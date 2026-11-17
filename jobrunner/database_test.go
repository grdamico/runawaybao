package jobrunner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type executeFunc func(context.Context, string) error

func (f executeFunc) Execute(ctx context.Context, id string) error { return f(ctx, id) }
func TestDatabaseAction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		ctx    context.Context
		id     string
		fail   bool
		want   Status
		calls  int
	}{
		{"authorized", NewPolicy(PermissionDBReadDemo), context.Background(), "id", false, Succeeded, 1},
		{"denied", NewPolicy(PermissionExecute, PermissionReadDemo), context.Background(), "id", false, Denied, 0},
		{"invalid", NewPolicy(PermissionDBReadDemo), context.Background(), " ", false, Failed, 0},
		{"canceled", NewPolicy(PermissionDBReadDemo), canceledContext(), "id", false, Failed, 0},
		{"nil context", NewPolicy(PermissionDBReadDemo), nil, "id", false, Failed, 0},
		{"failure", NewPolicy(PermissionDBReadDemo), context.Background(), "id", true, Failed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := NewRunnerWithDatabase(tc.policy, nil, executeFunc(func(ctx context.Context, id string) error {
				calls++
				if id != tc.id || ctx != tc.ctx {
					t.Fatal("execution context changed")
				}
				if tc.fail {
					return errors.New("sensitive-token password connection-string")
				}
				return nil
			}))
			result := r.RunContext(tc.ctx, Job{tc.id, ActionDBReadDemo})
			if result.Status != tc.want || calls != tc.calls {
				t.Fatal("unexpected execution")
			}
			body, _ := json.Marshal(result)
			if strings.Contains(string(body), "sensitive-token") || strings.Contains(string(body), "connection-string") {
				t.Fatal("raw error leaked")
			}
			if tc.id == "id" {
				if got := r.Run(Job{tc.id, ActionDBReadDemo}); got.Error != "duplicate job ID" {
					t.Fatal("duplicate accepted")
				}
				if calls != tc.calls {
					t.Fatal("duplicate called executor")
				}
			}
		})
	}
	if got := NewRunner(NewPolicy(PermissionDBReadDemo)).Run(Job{"missing", ActionDBReadDemo}); got.Status != Failed {
		t.Fatal("missing executor accepted")
	}
}
