// Package jobrunner executes deterministic, in-memory mock jobs.
package jobrunner

import (
	"strings"
	"sync"
)

const PermissionExecute = "mock:execute"

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
	mu     sync.Mutex
	policy Policy
	seen   map[string]bool
}

func NewRunner(policy Policy) *Runner {
	return &Runner{policy: policy, seen: make(map[string]bool)}
}

func (r *Runner) Run(job Job) JobResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := JobResult{JobID: job.ID, Status: Failed}
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.Action) == "" {
		result.Error = "id and action must be nonempty"
		return result
	}
	if r.seen[job.ID] {
		result.Error = "duplicate job ID"
		return result
	}
	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	r.seen[job.ID] = true
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
	case "succeed":
		return action{permission: PermissionExecute, status: Succeeded}, true
	case "fail":
		return action{permission: PermissionExecute, status: Failed, failure: "simulated execution failure"}, true
	default:
		return action{}, false
	}
}
