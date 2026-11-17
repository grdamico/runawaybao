// Package jobrunner executes authorized mock, demo-secret, and database actions.
package jobrunner

import (
	"context"
	"strings"
	"sync"

	"github.com/grdamico/runawaybao/openbao"
)

const PermissionExecute = "mock:execute"

const (
	ActionReadDemo       = "read-demo"
	PermissionReadDemo   = "secret:read-demo"
	ActionDBReadDemo     = "db-read-demo"
	PermissionDBReadDemo = "database:read-demo"
)

// DemoReader is implemented by the restricted OpenBao client. Implementations
// must honor context cancellation and be safe for concurrent calls.
// Returned credentials are discarded and never included in job results.
type DemoReader interface {
	ReadDemo(context.Context) (openbao.DemoSecret, error)
}

// DatabaseExecutor owns database credentials and lease lifecycle.
type DatabaseExecutor interface {
	Execute(context.Context, string) error
}

type Job struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Denied    Status = "denied"
)

// JobResult uses a string error so JSON audit records retain the error message.
type JobResult struct {
	JobID  string `json:"job_id"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Policy grants exact permissions. Its zero value denies everything.
// Construct a new policy to change grants; callers cannot mutate its set.
type Policy struct {
	grants map[string]bool
}

func NewPolicy(permissions ...string) Policy {
	p := Policy{grants: make(map[string]bool, len(permissions))}
	for _, permission := range permissions {
		if strings.TrimSpace(permission) != "" {
			p.grants[permission] = true
		}
	}
	return p
}

func (p Policy) Allows(permission string) bool {
	return permission != "" && p.grants[permission]
}

// Runner remembers every structurally valid submitted ID, including failures
// and denials. Duplicate protection is local to this instance, not persistent.
// Invalid requests do not consume IDs; unknown actions do.
// Run may be called concurrently. The zero value denies all supported actions.
type Runner struct {
	mu       sync.Mutex
	policy   Policy
	seen     map[string]bool
	reader   DemoReader
	database DatabaseExecutor
}

func NewRunner(policy Policy) *Runner {
	return &Runner{policy: policy, seen: make(map[string]bool)}
}

// NewRunnerWithReader enables the fixed secret-read action. Supply a client
// configured with a restricted token; a nil reader fails closed.
func NewRunnerWithReader(policy Policy, reader DemoReader) *Runner {
	r := NewRunner(policy)
	r.reader = reader
	return r
}

// NewRunnerWithDatabase adds the database action while preserving KV support.
// Revocation stays on the trusted manager, outside the job request API.
func NewRunnerWithDatabase(policy Policy, reader DemoReader, database DatabaseExecutor) *Runner {
	r := NewRunnerWithReader(policy, reader)
	r.database = database
	return r
}

// Run preserves the mock API. Use RunContext for cancellable secret reads.
func (r *Runner) Run(job Job) JobResult {
	return r.RunContext(context.Background(), job)
}

// RunContext reserves valid IDs before execution, including canceled requests.
// No dependency calls or network operations run under the duplicate-store lock.
func (r *Runner) RunContext(ctx context.Context, job Job) JobResult {
	result := JobResult{JobID: job.ID, Status: Failed}
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.Action) == "" {
		result.Error = "id and action must be nonempty"
		return result
	}
	r.mu.Lock()
	if r.seen[job.ID] {
		r.mu.Unlock()
		result.Error = "duplicate job ID"
		return result
	}
	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	r.seen[job.ID] = true
	r.mu.Unlock()
	action, known := lookupAction(job.Action)
	if !known {
		result.Error = "unknown action"
		return result
	}
	if !r.policy.Allows(action.permission) {
		result.Status = Denied
		result.Error = "permission denied"
		return result
	}
	if job.Action == ActionDBReadDemo {
		if ctx == nil || ctx.Err() != nil {
			result.Error = "invalid or canceled execution context"
			return result
		}
		if r.database == nil {
			result.Error = "database executor unavailable"
			return result
		}
		if r.database.Execute(ctx, job.ID) != nil {
			result.Error = "database job failed; inspect controller lifecycle state"
			return result
		}
		result.Status = Succeeded
		return result
	}
	if job.Action == ActionReadDemo {
		if ctx == nil {
			result.Error = "invalid execution context"
			return result
		}
		if ctx.Err() != nil {
			result.Error = "secret read canceled or deadline exceeded"
			return result
		}
		if r.reader == nil {
			result.Error = "secret reader unavailable"
			return result
		}
		_, err := r.reader.ReadDemo(ctx)
		if ctx.Err() != nil {
			result.Error = "secret read canceled or deadline exceeded"
			return result
		}
		if err != nil {
			result.Error = "secret read failed"
			return result
		}
		result.Status = Succeeded
		return result
	}
	// Mock execution is just a deterministic outcome, with no external effects.
	result.Status = action.status
	result.Error = action.failure
	return result
}

// action metadata is internal and cannot be supplied or overridden by a job.
type action struct {
	permission string
	status     Status
	failure    string
}

func lookupAction(name string) (action, bool) {
	switch name {
	case ActionDBReadDemo:
		return action{permission: PermissionDBReadDemo}, true
	case ActionReadDemo:
		return action{permission: PermissionReadDemo}, true
	case "succeed":
		return action{permission: PermissionExecute, status: Succeeded}, true
	case "fail":
		return action{permission: PermissionExecute, status: Failed, failure: "simulated execution failure"}, true
	default:
		return action{}, false
	}
}
