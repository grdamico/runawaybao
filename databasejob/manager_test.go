package databasejob

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/openbao"
)

type issueFunc func(context.Context) (openbao.DatabaseCredentials, error)

func (f issueFunc) Issue(c context.Context) (openbao.DatabaseCredentials, error) { return f(c) }

type revokeFunc func(context.Context, string) error

func (f revokeFunc) Revoke(c context.Context, id string) error { return f(c, id) }

type readFunc func(context.Context, openbao.DatabaseCredentials) error

func (f readFunc) Read(c context.Context, s openbao.DatabaseCredentials) error { return f(c, s) }
func fixture(id string) openbao.DatabaseCredentials {
	return openbao.DatabaseCredentials{LeaseID: id, Username: "sensitive-user", Password: "sensitive-password", TTL: time.Minute}
}
func newManager(t *testing.T, i Issuer, r Revoker, q Reader) *Manager {
	t.Helper()
	m, err := New(i, r, q)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIndependentLeasesAndRetry(t *testing.T) {
	var issued atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	var mu sync.Mutex
	counts := map[string]int{}
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
		if issued.Add(1) == 1 {
			return fixture("lease-A"), nil
		}
		return fixture("lease-B"), nil
	}), revokeFunc(func(_ context.Context, id string) error {
		mu.Lock()
		counts[id]++
		mu.Unlock()
		if id == "lease-A" && fail.Load() {
			return errors.New("sensitive-token")
		}
		return nil
	}), readFunc(func(context.Context, openbao.DatabaseCredentials) error { return nil }))
	ctx := context.Background()
	if m.Execute(ctx, "A") != nil || m.Execute(ctx, "B") != nil {
		t.Fatal("execution failed")
	}
	if m.Execute(ctx, "A") != ErrDuplicate {
		t.Fatal("duplicate issued")
	}
	if err := m.RevokeJob(ctx, "A"); err != ErrRevoke {
		t.Fatal("failure not reported safely")
	}
	a, _ := m.Lookup("A")
	b, _ := m.Lookup("B")
	if a.State != RevocationFailed || b.State != Active || b.LeaseID != "lease-B" {
		t.Fatal("cross-job state corruption")
	}
	if m.Read(ctx, "A") != ErrInactive || m.Read(ctx, "B") != nil {
		t.Fatal("invalid read lifecycle")
	}
	fail.Store(false)
	if m.RevokeJob(ctx, "A") != nil || m.RevokeJob(ctx, "A") != nil {
		t.Fatal("retry/idempotence failed")
	}
	if counts["lease-A"] != 2 || counts["lease-B"] != 0 {
		t.Fatal("wrong lease revoked")
	}
	if m.Read(ctx, "B") != nil {
		t.Fatal("B affected")
	}
	if m.Close(ctx) != nil || m.Close(ctx) != nil {
		t.Fatal("cleanup failed")
	}
	if m.Execute(ctx, "C") != ErrClosed {
		t.Fatal("closed manager accepted execution")
	}
	if issued.Load() != 2 {
		t.Fatal("unexpected issuance")
	}
}

func TestFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		partial    bool
		issueFail  bool
		revokeFail bool
		want       error
	}{
		{"query", false, false, false, ErrRead},
		{"query cleanup failed", false, false, true, ErrCleanup},
		{"issuance", false, true, false, ErrIssue},
		{"partial issuance", true, true, false, ErrIssue},
		{"partial cleanup failed", true, true, true, ErrCleanup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
				if tc.issueFail {
					if tc.partial {
						return fixture("A"), errors.New("sensitive-password")
					}
					return openbao.DatabaseCredentials{}, errors.New("sensitive-password")
				}
				return fixture("A"), nil
			}), revokeFunc(func(ctx context.Context, id string) error {
				calls++
				if ctx.Err() != nil || id != "A" {
					t.Fatal("invalid cleanup")
				}
				if tc.revokeFail {
					return errors.New("sensitive-token")
				}
				return nil
			}), readFunc(func(context.Context, openbao.DatabaseCredentials) error { return errors.New("sensitive-connection") }))
			if err := m.Execute(context.Background(), "A"); err != tc.want {
				t.Fatal("wrong safe failure")
			}
			wantCalls := 1
			if tc.issueFail && !tc.partial {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatal("cleanup count")
			}
			record, _ := m.Lookup("A")
			if tc.revokeFail && record.State != RevocationFailed {
				t.Fatal("failure lost")
			}
			if !tc.issueFail || tc.partial {
				if !tc.revokeFail && record.State != Revoked {
					t.Fatal("cleanup not recorded")
				}
			}
		})
	}
}

func TestCancellationAndConcurrentIsolation(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	var issued atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
		if issued.Add(1) == 1 {
			return fixture("A"), nil
		}
		return fixture("B"), nil
	}), revokeFunc(func(ctx context.Context, id string) error {
		if ctx.Err() != nil || id != "A" {
			return errors.New("bad cleanup context")
		}
		return nil
	}), readFunc(func(ctx context.Context, c openbao.DatabaseCredentials) error {
		if c.LeaseID == "A" {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}))
	done := make(chan error, 1)
	go func() { done <- m.Execute(ctx, "A") }()
	<-entered
	go func() {
		defer close(released)
		if m.Execute(context.Background(), "B") != nil {
			t.Error("B blocked/failed")
		}
	}()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("global lock held")
	}
	if m.RevokeJob(context.Background(), "A") != ErrBusy {
		t.Fatal("overlapping lifecycle accepted")
	}
	if m.Execute(context.Background(), "A") != ErrDuplicate {
		t.Fatal("in-flight duplicate accepted")
	}
	cancel()
	select {
	case err := <-done:
		if err != ErrRead {
			t.Fatal("cancellation cleanup failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not canceled")
	}
	a, _ := m.Lookup("A")
	b, _ := m.Lookup("B")
	if a.State != Revoked || b.State != Active {
		t.Fatal("cancellation affected B")
	}
}

func TestExpiryIsNotConfirmation(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
		c := fixture("expired")
		c.TTL = time.Nanosecond
		return c, nil
	}), revokeFunc(func(context.Context, string) error {
		if fail.Load() {
			return errors.New("unconfirmed")
		}
		return nil
	}), readFunc(func(context.Context, openbao.DatabaseCredentials) error { return nil }))
	if m.Execute(context.Background(), "expired") != nil {
		t.Fatal("execute")
	}
	if m.RevokeJob(context.Background(), "expired") != ErrRevoke {
		t.Fatal("TTL treated as confirmation")
	}
	fail.Store(false)
	if m.RevokeJob(context.Background(), "expired") != nil {
		t.Fatal("backend expiry acknowledgement not accepted")
	}
}

func TestCloseContinuesAfterFailure(t *testing.T) {
	var count int
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
		count++
		if count == 1 {
			return fixture("A"), nil
		}
		return fixture("B"), nil
	}), revokeFunc(func(_ context.Context, id string) error {
		if id == "A" {
			return ErrRevoke
		}
		return nil
	}), readFunc(func(context.Context, openbao.DatabaseCredentials) error { return nil }))
	_ = m.Execute(context.Background(), "A")
	_ = m.Execute(context.Background(), "B")
	if m.Close(context.Background()) != ErrCleanup {
		t.Fatal("cleanup failure hidden")
	}
	b, _ := m.Lookup("B")
	if b.State != Revoked {
		t.Fatal("B cleanup skipped")
	}
}

func TestIssuanceFailureDoesNotAffectOtherJob(t *testing.T) {
	var count int
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) {
		count++
		if count == 1 {
			return fixture("B"), nil
		}
		return openbao.DatabaseCredentials{}, errors.New("sensitive-issuance-error")
	}), revokeFunc(func(context.Context, string) error { t.Fatal("unrelated lease revoked"); return nil }), readFunc(func(_ context.Context, c openbao.DatabaseCredentials) error {
		if c.LeaseID != "B" {
			t.Fatal("wrong credentials")
		}
		return nil
	}))
	ctx := context.Background()
	if m.Execute(ctx, "B") != nil {
		t.Fatal("B failed")
	}
	before, _ := m.Lookup("B")
	if m.Execute(ctx, "A") != ErrIssue {
		t.Fatal("issuance failure not reported")
	}
	after, _ := m.Lookup("B")
	if before != after || m.Read(ctx, "B") != nil {
		t.Fatal("A issuance failure affected B")
	}
	if m.RevokeJob(ctx, "missing") != ErrNotFound {
		t.Fatal("unknown job treated as revoked")
	}
	if m.RevokeJob(ctx, "A") != ErrInactive {
		t.Fatal("unknown issuance outcome treated as revoked")
	}
}

func TestCancellationAfterIssuanceCleansLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var revoked bool
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) { cancel(); return fixture("A"), nil }), revokeFunc(func(ctx context.Context, id string) error {
		if ctx.Err() != nil || id != "A" {
			t.Fatal("cleanup reused canceled context")
		}
		revoked = true
		return nil
	}), readFunc(func(context.Context, openbao.DatabaseCredentials) error {
		t.Fatal("canceled execution read database")
		return nil
	}))
	if m.Execute(ctx, "A") != ErrRead || !revoked {
		t.Fatal("post-issuance cancellation leaked lease")
	}
}

func TestImmutableWorkloadBinding(t *testing.T) {
	m := newManager(t, issueFunc(func(context.Context) (openbao.DatabaseCredentials, error) { return fixture("lease"), nil }), revokeFunc(func(context.Context, string) error { t.Fatal("binding revoked lease"); return nil }), readFunc(func(context.Context, openbao.DatabaseCredentials) error { return nil }))
	if m.Execute(context.Background(), "A") != nil {
		t.Fatal("execute")
	}
	id := strings.Repeat("a", 64)
	if m.BindWorkload("A", id) != nil {
		t.Fatal("bind")
	}
	if m.BindWorkload("A", strings.Repeat("b", 64)) != ErrDuplicate {
		t.Fatal("rebound workload")
	}
	record, _ := m.Lookup("A")
	if record.WorkloadID != id || record.LeaseID != "lease" || record.State != Active {
		t.Fatal("lease corrupted")
	}
}
