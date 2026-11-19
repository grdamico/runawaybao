package enforcement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type revokeFunc func(context.Context, string) error

func (f revokeFunc) Revoke(ctx context.Context, id string) error { return f(ctx, id) }

const testLease = "private/creds/reader/issued-once"

var testEvent = strings.Repeat("a", 64)
var testExecution = strings.Repeat("b", 32)

func TestOneAttemptAndRestart(t *testing.T) {
	for _, failure := range []error{nil, context.DeadlineExceeded} {
		t.Run(map[bool]string{true: "acknowledged", false: "uncertain"}[failure == nil], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "journal")
			calls := 0
			revoker := revokeFunc(func(_ context.Context, lease string) error {
				calls++
				if lease != testLease {
					t.Fatal("lease changed")
				}
				return failure
			})
			audit := func(r Record) {
				if strings.Contains(r.Event+r.Execution+r.Operation+r.Reason, testLease) {
					t.Fatal("private lease leaked")
				}
			}
			run := strings.Repeat("c", 32)
			if _, err := NewLocal(run, "lab", "incorrect", path, revoker, audit); !errors.Is(err, ErrAuthorization) {
				t.Fatal("invalid local grant accepted")
			}
			reserve := func(use func(Target, func() bool) error) error {
				return use(Target{Execution: testExecution, Lease: testLease, PolicyDigest: testEvent}, func() bool { return true })
			}
			c, err := NewLocal(run, "lab", "lab "+run, path, revoker, audit)
			if err != nil {
				t.Fatal(err)
			}
			want := "revocation_acknowledged"
			if failure != nil {
				want = "revocation_outcome_uncertain"
			}
			if got := c.Consider(context.Background(), testEvent, reserve); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
			c.Consider(context.Background(), testEvent, reserve)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			run = strings.Repeat("d", 32)
			c, err = NewLocal(run, "lab", "lab "+run, path, revoker, audit)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.Consider(context.Background(), testEvent, reserve)
			if calls != 1 {
				t.Fatalf("sent %d calls", calls)
			}
		})
	}
}

func TestDispatchRechecksAndPersistence(t *testing.T) {
	for _, mode := range []string{"ineligible", "changed_after_intent", "persistence_failed"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			run := strings.Repeat("e", 32)
			c, err := NewLocal(run, "lab", "lab "+run, filepath.Join(dir, "journal"), revokeFunc(func(context.Context, string) error {
				t.Error("request sent without a current durable authorization")
				return nil
			}), func(Record) {})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			checks := 0
			if mode == "persistence_failed" {
				_ = c.journal.file.Close()
			}
			c.Consider(context.Background(), testEvent, func(use func(Target, func() bool) error) error {
				return use(Target{Execution: testExecution, Lease: testLease, PolicyDigest: testEvent}, func() bool {
					checks++
					return mode == "persistence_failed" || (mode == "changed_after_intent" && checks == 1)
				})
			})
		})
	}
}

func TestInterruptedDispatchAndJournalLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openJournal(path); err == nil {
		second.file.Close()
		t.Fatal("second controller opened a live journal")
	}
	e := entry{Run: strings.Repeat("f", 32), Execution: testExecution, Event: testEvent,
		Operation: strings.Repeat("2", 32), Lease: testLease}
	if j.append(e, "authorized") != nil || j.append(e, "dispatch_intent") != nil || j.file.Close() != nil {
		t.Fatal("journal setup failed")
	}
	run := strings.Repeat("1", 32)
	c, err := NewLocal(run, "lab", "lab "+run, path, revokeFunc(func(context.Context, string) error {
		t.Fatal("interrupted request was retried")
		return nil
	}), func(Record) {})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Consider(context.Background(), testEvent, func(use func(Target, func() bool) error) error {
		return use(Target{Execution: testExecution, Lease: testLease, PolicyDigest: testEvent}, func() bool { return true })
	})
}

func TestFailedResultPersistenceBlocksOtherOperations(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var c *Controller
	calls := 0
	revoker := revokeFunc(func(context.Context, string) error {
		calls++
		_ = c.journal.file.Close()
		return nil
	})
	run := strings.Repeat("e", 32)
	var err error
	c, err = NewLocal(run, "lab", "lab "+run, filepath.Join(dir, "journal"), revoker, func(Record) {})
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{testLease, "other/creds/reader/lease"} {
		event := strings.Repeat(string('a'+rune(index)), 64)
		got := c.Consider(context.Background(), event, func(use func(Target, func() bool) error) error {
			return use(Target{Execution: testExecution, Lease: id, PolicyDigest: testEvent}, func() bool { return true })
		})
		if id == testLease && got != "revocation_outcome_uncertain" {
			t.Fatal("lost persistence was presented as confirmed")
		}
	}
	if calls != 1 {
		t.Fatal("another operation was dispatched after loss of durability")
	}
}
