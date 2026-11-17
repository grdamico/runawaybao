package jobrunner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/openbao"
)

type readerFunc func(context.Context) (openbao.DemoSecret, error)

func (f readerFunc) ReadDemo(ctx context.Context) (openbao.DemoSecret, error) { return f(ctx) }

func TestSecretAuthorizationAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		reader DemoReader
		want   JobResult
	}{
		{"authorized", NewPolicy(PermissionReadDemo), readerFunc(func(context.Context) (openbao.DemoSecret, error) {
			return openbao.DemoSecret{Username: "sensitive-user", Password: "sensitive-password"}, nil
		}), JobResult{"id", Succeeded, ""}},
		{"denied", NewPolicy(PermissionExecute), readerFunc(func(context.Context) (openbao.DemoSecret, error) {
			t.Fatal("denied reader called")
			return openbao.DemoSecret{}, nil
		}), JobResult{"id", Denied, "permission denied"}},
		{"missing reader", NewPolicy(PermissionReadDemo), nil, JobResult{"id", Failed, "secret reader unavailable"}},
		{"sensitive failure", NewPolicy(PermissionReadDemo), readerFunc(func(context.Context) (openbao.DemoSecret, error) {
			return openbao.DemoSecret{Password: "sensitive-password"}, errors.New("sensitive-token sensitive-password")
		}), JobResult{"id", Failed, "secret read failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRunnerWithReader(tc.policy, tc.reader)
			got := r.RunContext(context.Background(), Job{"id", ActionReadDemo})
			if got != tc.want {
				t.Fatalf("unexpected safe result: %+v", got)
			}
			if duplicate := r.Run(Job{"id", ActionReadDemo}); duplicate.Error != "duplicate job ID" {
				t.Fatal("completed ID was not reserved")
			}
			data, err := json.Marshal(got)
			if err != nil || strings.Contains(string(data), "sensitive-") {
				t.Fatal("unsafe result")
			}
		})
	}
}

func TestSecretValidationAndCancellationBeforeRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		job  Job
		want string
	}{
		{"invalid request", context.Background(), Job{"id", ""}, "id and action must be nonempty"},
		{"invalid ID", context.Background(), Job{" ", ActionReadDemo}, "id and action must be nonempty"},
		{"nil context", nil, Job{"id", ActionReadDemo}, "invalid execution context"},
		{"canceled", canceledContext(), Job{"id", ActionReadDemo}, "secret read canceled or deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRunnerWithReader(NewPolicy(PermissionReadDemo), readerFunc(func(context.Context) (openbao.DemoSecret, error) {
				t.Fatal("reader called")
				return openbao.DemoSecret{}, nil
			}))
			if got := r.RunContext(tc.ctx, tc.job); got.Status != Failed || got.Error != tc.want {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestSecretInFlightDuplicateAndCancellation(t *testing.T) {
	started := make(chan struct{})
	r := NewRunnerWithReader(NewPolicy(PermissionReadDemo, PermissionExecute), readerFunc(func(ctx context.Context) (openbao.DemoSecret, error) {
		close(started)
		<-ctx.Done()
		return openbao.DemoSecret{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan JobResult, 1)
	go func() { done <- r.RunContext(ctx, Job{"id", ActionReadDemo}) }()
	<-started
	// Both operations must finish while the network-like read is blocked.
	checks := make(chan bool, 1)
	go func() {
		dup := r.Run(Job{"id", ActionReadDemo})
		other := r.Run(Job{"other", "succeed"})
		checks <- dup.Error == "duplicate job ID" && other.Status == Succeeded
	}()
	select {
	case ok := <-checks:
		if !ok {
			t.Fatal("duplicate or independent execution failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader holds runner lock")
	}
	cancel()
	select {
	case got := <-done:
		if got.Status != Failed || got.Error != "secret read canceled or deadline exceeded" {
			t.Fatalf("unexpected result: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not complete")
	}
	if got := r.Run(Job{"id", ActionReadDemo}); got.Error != "duplicate job ID" {
		t.Fatal("canceled ID was not reserved")
	}
}

func TestSecretExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	r := NewRunnerWithReader(NewPolicy(PermissionReadDemo), readerFunc(func(context.Context) (openbao.DemoSecret, error) {
		t.Fatal("expired read called")
		return openbao.DemoSecret{}, nil
	}))
	if got := r.RunContext(ctx, Job{"deadline", ActionReadDemo}); got.Error != "secret read canceled or deadline exceeded" {
		t.Fatalf("unexpected result: %+v", got)
	}
}
