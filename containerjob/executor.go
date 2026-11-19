// Package containerjob coordinates trusted workload admission and lifecycle.
// Its explicit reservation lets the trusted coordinator revalidate admission.
package containerjob

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/enforcement"
	"github.com/grdamico/runawaybao/executionpolicy"
	"github.com/grdamico/runawaybao/policyaudit"
	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
)

var ErrExecution = errors.New("container database operation failed; diagnostics suppressed")

func Policies() []executionpolicy.Policy {
	return []executionpolicy.Policy{
		{ID: "container-reader", Version: "v1", AllowedExecutables: []string{"/runawaybao-db-worker", "/runawaybao-netprobe"}},
	}
}

type Execution struct {
	session                   *workload.Session
	identity                  *workload.Identity
	PID                       uint32
	executing                 bool
	active                    bool
	lifecycle                 sync.Mutex
	executionID, policyDigest string
}
type Executor struct {
	mu            sync.Mutex
	manager       *databasejob.Manager
	docker        *workload.Database
	correlator    *runtimeevents.Correlator
	audit         *policyaudit.Adapter
	jobs          map[string]*Execution
	identityAgent string
	observation   *runtimeevents.Consumer
}

func New(m *databasejob.Manager, d *workload.Database, c *runtimeevents.Correlator, a *policyaudit.Adapter) (*Executor, error) {
	if m == nil || d == nil || c == nil || a == nil {
		return nil, ErrExecution
	}
	// Validate the exact immutable profile before any workload is admitted.
	for _, p := range Policies() {
		if _, err := executionpolicy.Compile(p); err != nil {
			return nil, ErrExecution
		}
	}
	return &Executor{manager: m, docker: d, correlator: c, audit: a, jobs: make(map[string]*Execution)}, nil
}

// NewWithIdentity enables only namespace assurance. The worker's bootstrap
// waits for credentials until its identity and observation generation are bound.
func NewWithIdentity(m *databasejob.Manager, d *workload.Database, c *runtimeevents.Correlator, a *policyaudit.Adapter, agent string, observation *runtimeevents.Consumer) (*Executor, error) {
	e, err := New(m, d, c, a)
	if err != nil || !workload.IDPattern.MatchString(agent) || observation == nil {
		return nil, ErrExecution
	}
	e.identityAgent, e.observation = agent, observation
	return e, nil
}

func (e *Executor) Execute(ctx context.Context, job string) error {
	if !executionpolicy.ValidIdentifier(job) {
		return ErrExecution
	}
	executionID, err := enforcement.NewRun()
	if err != nil {
		return ErrExecution
	}
	e.mu.Lock()
	if _, ok := e.jobs[job]; ok || len(e.jobs) >= 32 {
		e.mu.Unlock()
		return ErrExecution
	}
	x := &Execution{executing: true, executionID: executionID}
	e.jobs[job] = x
	e.mu.Unlock()
	defer func() { e.mu.Lock(); x.executing = false; e.mu.Unlock() }()
	if e.manager.Prepare(ctx, job) != nil {
		return ErrExecution
	}
	r, err := e.docker.Create(ctx, job)
	if err != nil {
		return ErrExecution
	}
	if e.manager.BindWorkload(job, r.ContainerID) != nil || e.correlator.Register(r.ContainerID, job) != nil || e.audit.Bind(policyaudit.Binding{JobID: job, WorkloadID: r.ContainerID, PolicyID: "container-reader", PolicyVersion: "v1"}) != nil {
		return ErrExecution
	}
	x.policyDigest = e.audit.PolicyDigest(job, r.ContainerID, "container-reader", "v1")
	var coverage runtimeevents.Coverage
	if e.observation != nil {
		coverage = e.observation.Coverage()
		if coverage.Status != "observing" {
			return ErrExecution
		}
	}
	s, info, err := e.docker.Start(ctx, job)
	if err != nil {
		return ErrExecution
	}
	e.mu.Lock()
	x.session = s
	e.mu.Unlock()
	if e.observation != nil {
		proof, err := e.docker.PinIdentity(ctx, e.identityAgent, job, info)
		if err != nil {
			return ErrExecution
		}
		if coverage != e.observation.Coverage() || e.correlator.ActivateIdentity(info, proof, coverage) != nil {
			proof.Close()
			return ErrExecution
		}
		e.mu.Lock()
		x.identity = proof
		e.mu.Unlock()
	} else if e.correlator.Activate(info) != nil {
		return ErrExecution
	}
	if e.manager.Deliver(ctx, job, s.Deliver) != nil {
		return ErrExecution
	}
	pid, err := s.Read(ctx)
	if err != nil {
		return ErrExecution
	}
	e.mu.Lock()
	x.PID = pid
	x.active = true
	e.mu.Unlock()
	return nil
}
func (e *Executor) session(job string) (*workload.Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	x, ok := e.jobs[job]
	if !ok || x.session == nil {
		return nil, false
	}
	return x.session, true
}
func (e *Executor) PID(job string) uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if x := e.jobs[job]; x != nil {
		return x.PID
	}
	return 0
}
func (e *Executor) Read(ctx context.Context, job string) error {
	s, ok := e.session(job)
	if !ok {
		return ErrExecution
	}
	pid, err := s.Read(ctx)
	if err != nil {
		return ErrExecution
	}
	e.mu.Lock()
	e.jobs[job].PID = pid
	e.mu.Unlock()
	return nil
}
func (e *Executor) Child(ctx context.Context, job string) error {
	s, ok := e.session(job)
	if !ok || s.Child(ctx) != nil {
		return ErrExecution
	}
	return nil
}

// acquireLifecycle orders reservations as lifecycle, manager entry, then
// enforcement journal. The executor map mutex is held only for snapshots.
func (e *Executor) acquireLifecycle(job string) (*Execution, bool) {
	e.mu.Lock()
	x := e.jobs[job]
	busy := x != nil && x.executing
	e.mu.Unlock()
	if busy || (x != nil && !x.lifecycle.TryLock()) {
		return nil, false
	}
	return x, true
}

// Consider evaluates only notices selected by the trusted controller's
// observation loop. It re-evaluates the exact event under the pinned policy.
func (e *Executor) Consider(ctx context.Context, controller *enforcement.Controller, n runtimeevents.Notice, r runtimeevents.Result, d executionpolicy.Decision) string {
	if controller == nil {
		return "enforcement_disabled"
	}
	return controller.Consider(ctx, d.EventID, func(use func(enforcement.Target, func() bool) error) error {
		if e.observation == nil || !e.observation.OwnsNotice(n) {
			return enforcement.ErrAuthorization
		}
		if r.Reason == "duplicate_event" || r.OutOfOrder || r.AssuranceReason == "event_freshness_unverified" {
			return enforcement.ErrObservation
		}
		coverage := e.observation.EffectiveCoverage(n)
		if coverage.Status != "observing" || coverage.Generation != d.Coverage.Generation || r.AssuranceReason == "coverage_unverified" {
			return enforcement.ErrCoverage
		}
		if r.Assurance != runtimeevents.VerifiedIdentity {
			return enforcement.ErrIdentity
		}
		if d.Outcome != executionpolicy.Suspicious || d.Rule != "exec.allowlist" || d.Reason != "executable_not_allowed" {
			return enforcement.ErrAuthorization
		}
		x, ok := e.acquireLifecycle(r.JobID)
		if !ok || x == nil {
			return enforcement.ErrAuthorization
		}
		defer x.lifecycle.Unlock()
		if !x.active || x.policyDigest == "" {
			return enforcement.ErrIdentity
		}
		var result error
		ok = e.manager.WithWorkloadLease(r.JobID, r.ContainerID, func(lease databasejob.Record) {
			recheck := func() bool {
				check, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				inventory, err := workload.Inventory(check)
				coverage := e.observation.EffectiveCoverage(n)
				if err != nil || x.policyDigest != e.audit.PolicyDigest(r.JobID, r.ContainerID, d.PolicyID, d.PolicyVersion) ||
					!e.correlator.RecheckIdentity(*n.Event, r, inventory, coverage) {
					return false
				}
				current := e.audit.Evaluate(*n.Event, r, coverage)
				return current == d && e.manager.WorkloadLeaseCurrent(lease) && time.Until(lease.IssuedAt.Add(lease.TTL)) > 10*time.Second
			}
			result = use(enforcement.Target{Execution: x.executionID, Lease: lease.LeaseID, PolicyDigest: x.policyDigest}, recheck)
		})
		if !ok {
			return enforcement.ErrAuthorization
		}
		return result
	})
}

// Cleanup is an explicit controller lifecycle action, independent of decisions.
// Resource and lease metadata remain available after every failure and retry.
func (e *Executor) Cleanup(ctx context.Context, job string) error {
	x, ok := e.acquireLifecycle(job)
	if !ok {
		return ErrExecution
	}
	if x != nil {
		defer x.lifecycle.Unlock()
		x.active = false
		if x.identity != nil {
			x.identity.Close()
		}
	}
	if s, ok := e.session(job); ok {
		_ = s.Close()
	}
	r, ok := e.docker.Lookup(job)
	if !ok {
		return nil
	}
	if info, err := workload.Inspect(ctx, r.ContainerID); err == nil && !info.Running {
		_ = e.correlator.Retire(info)
	}
	return e.docker.Cleanup(ctx, job)
}

// Finish requests normal protocol completion. It is a lifecycle operation only.
func (e *Executor) Finish(ctx context.Context, job string) error {
	x, ok := e.acquireLifecycle(job)
	if !ok || x == nil {
		return ErrExecution
	}
	defer x.lifecycle.Unlock()
	x.active = false
	s, ok := e.session(job)
	if !ok || s.Finish(ctx) != nil {
		return ErrExecution
	}
	return nil
}
