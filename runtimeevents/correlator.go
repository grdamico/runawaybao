package runtimeevents

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/workload"
)

var ErrBinding = errors.New("invalid or duplicate workload binding")

type Assurance string

const (
	UnknownIdentity     Assurance = "unknown"
	ObservationOnly     Assurance = "observation_only"
	ConflictingIdentity Assurance = "conflicting"
	VerifiedIdentity    Assurance = "verified"
)

var shortID = regexp.MustCompile(`^[a-f0-9]{31}$`)

type Metadata struct{ JobID, WorkloadID, LeaseID string }
type Lookup func(string) (Metadata, bool)
type binding struct {
	job                        string
	started, finished, retired time.Time
	latest                     time.Time
	invalid                    bool
	identity                   func(workload.Container) (uint32, uint32, bool)
	pidNS, cgroupNS            uint32
	coverage                   Coverage
}
type Result struct {
	Assurance                                                         Assurance
	AssuranceReason                                                   string
	Status, Reason, JobID, LeaseID, ContainerID, ExecID, ParentExecID string
	Late                                                              bool
	Terminated, OutOfOrder                                            bool
}

// Correlator accepts only read-only metadata lookup, not a database manager or
// revoker. Capacity is fixed; exhaustion refuses admission rather than guessing.
type Correlator struct {
	mu       sync.Mutex
	lookup   Lookup
	bindings map[string]binding
	seen     map[string]time.Time
}

func NewCorrelator(lookup Lookup) *Correlator {
	return &Correlator{lookup: lookup, bindings: make(map[string]binding), seen: make(map[string]time.Time)}
}
func (c *Correlator) prune(now time.Time) {
	for id, b := range c.bindings {
		if !b.retired.IsZero() && now.Sub(b.retired) > 2*time.Minute {
			delete(c.bindings, id)
		}
	}
	for key, t := range c.seen {
		if now.Sub(t) > 2*time.Minute {
			delete(c.seen, key)
		}
	}
}
func (c *Correlator) Register(id, job string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(time.Now())
	if !workload.IDPattern.MatchString(id) || job == "" || c.lookup == nil || len(c.bindings) >= 64 {
		return ErrBinding
	}
	if _, ok := c.bindings[id]; ok {
		return ErrBinding
	}
	for _, b := range c.bindings {
		if b.job == job {
			return ErrBinding
		}
	}
	m, ok := c.lookup(job)
	if !ok || m.JobID != job || m.WorkloadID != id || m.LeaseID == "" {
		return ErrBinding
	}
	c.bindings[id] = binding{job: job}
	return nil
}
func (c *Correlator) Activate(info workload.Container) error {
	return c.activate(info, nil, Coverage{})
}

func (c *Correlator) ActivateIdentity(info workload.Container, identity *workload.Identity, coverage Coverage) error {
	if identity == nil || coverage.Status != "observing" {
		return ErrBinding
	}
	return c.activate(info, identity.Namespaces, coverage)
}

func (c *Correlator) activate(info workload.Container, identity func(workload.Container) (uint32, uint32, bool), coverage Coverage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.bindings[info.ID]
	if !ok || !b.started.IsZero() || !info.Running || info.StartedAt.IsZero() || info.Restarts != 0 {
		return ErrBinding
	}
	if identity != nil {
		pid, cgroup, ok := identity(info)
		if !ok {
			return ErrBinding
		}
		for _, existing := range c.bindings {
			if existing.identity != nil && (pid == existing.pidNS || cgroup == existing.cgroupNS) {
				return ErrBinding
			}
		}
		b.pidNS, b.cgroupNS, b.identity, b.coverage = pid, cgroup, identity, coverage
	}
	b.started = info.StartedAt
	c.bindings[info.ID] = b
	return nil
}
func (c *Correlator) Retire(info workload.Container) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.bindings[info.ID]
	if !ok || info.FinishedAt.IsZero() || info.Running {
		return ErrBinding
	}
	b.finished = info.FinishedAt
	b.retired = time.Now()
	c.bindings[info.ID] = b
	return nil
}
func normalizedFull(s string) string {
	if strings.HasPrefix(s, "docker://") {
		s = strings.TrimPrefix(s, "docker://")
	} else if strings.HasPrefix(s, "containerd://") {
		s = strings.TrimPrefix(s, "containerd://")
	}
	if !workload.IDPattern.MatchString(s) {
		return ""
	}
	return s
}
func (c *Correlator) Resolve(e Event, inventory []workload.Container, inventoryOK bool) Result {
	return c.ResolveObserved(e, inventory, inventoryOK, Coverage{})
}

func (c *Correlator) ResolveObserved(e Event, inventory []workload.Container, inventoryOK bool, coverage Coverage) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.prune(now)
	result := Result{Assurance: UnknownIdentity, Status: "uncorrelated", ExecID: e.Process.ExecID, ParentExecID: e.Process.ParentExecID}
	reject := func(reason string) Result {
		result.Reason, result.AssuranceReason = reason, reason
		if reason == "conflicting_identity" {
			result.Assurance = ConflictingIdentity
		}
		return result
	}
	if !inventoryOK {
		return reject("inventory_unavailable")
	}
	id := e.Process.Docker
	if id != "" && !shortID.MatchString(id) && !workload.IDPattern.MatchString(id) {
		return reject("unsupported_container_identity")
	}
	full := ""
	if e.FullID != "" {
		full = normalizedFull(e.FullID)
		if full == "" {
			return reject("invalid_full_identity")
		}
		if id != "" && !strings.HasPrefix(full, id) {
			return reject("conflicting_identity")
		}
		id = full
	}
	if id == "" {
		return reject("missing_container_identity")
	}
	// Include all current containers and retained bindings when checking uniqueness.
	candidates := make(map[string]bool)
	byID := make(map[string]workload.Container)
	for _, info := range inventory {
		if !workload.IDPattern.MatchString(info.ID) {
			return reject("invalid_inventory")
		}
		byID[info.ID] = info
		if strings.HasPrefix(info.ID, id) {
			candidates[info.ID] = true
		}
	}
	for known := range c.bindings {
		if strings.HasPrefix(known, id) {
			candidates[known] = true
		}
	}
	if len(candidates) != 1 {
		if len(candidates) > 1 {
			return reject("ambiguous_container_identity")
		}
		return reject("unknown_container")
	}
	for candidate := range candidates {
		id = candidate
	}
	b, ok := c.bindings[id]
	if !ok {
		return reject("unregistered_container")
	}
	if b.started.IsZero() || b.invalid {
		return reject("inactive_binding")
	}
	info, present := byID[id]
	if present && (info.Restarts != 0 || !info.StartedAt.Equal(b.started)) {
		b.invalid = true
		c.bindings[id] = b
		return reject("container_generation_changed")
	}
	if !present && b.retired.IsZero() {
		return reject("container_missing")
	}
	ended := b.finished
	if present && !info.Running {
		ended = info.FinishedAt
		if ended.IsZero() {
			return reject("lifecycle_unknown")
		}
	}
	if e.Process.ExecID == "" || e.Process.Start.IsZero() || e.Time.IsZero() {
		return reject("missing_execution_identity")
	}
	// Envelope time and WSL receipt time are not execution clocks. Live Tetragon
	// envelope timestamps can precede process.start_time. Validate the process
	// execution time against the Docker instance, without widening its lifetime.
	if e.Process.Start.Before(b.started) {
		return reject("outside_workload_lifetime")
	}
	if !ended.IsZero() && e.Process.Start.After(ended) {
		return reject("outside_workload_lifetime")
	}
	m, ok := c.lookup(b.job)
	if !ok || m.JobID != b.job || m.WorkloadID != id || m.LeaseID == "" {
		return reject("binding_changed")
	}
	key := e.Node + "\x00exec\x00" + e.Process.ExecID
	if _, ok := c.seen[key]; ok {
		return reject("duplicate_event")
	}
	if len(c.seen) >= 4096 {
		return reject("dedup_capacity")
	}
	c.seen[key] = now
	result.Status = "correlated"
	result.JobID = b.job
	result.LeaseID = m.LeaseID
	result.ContainerID = id
	result.Terminated = !ended.IsZero()
	result.OutOfOrder = !b.latest.IsZero() && e.Process.Start.Before(b.latest)
	result.Late = result.Terminated || result.OutOfOrder
	result.Assurance, result.AssuranceReason = assessIdentity(b, e, info, result.Late, coverage, now)
	if e.Process.Start.After(b.latest) {
		b.latest = e.Process.Start
		c.bindings[id] = b
	}
	return result
}

// NeedsInventory avoids Docker lookups for events that cannot match any binding.
// A potentially matching event always requires fresh complete inventory, including
// unrelated containers, before Resolve may attribute it.
func (c *Correlator) NeedsInventory(e Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.bindings {
		if e.Process.Docker != "" && strings.HasPrefix(id, e.Process.Docker) {
			return true
		}
		if e.FullID != "" && normalizedFull(e.FullID) == id {
			return true
		}
	}
	return false
}

// Only execs within the bounded freshness window can support strong identity.
// The lab allows for Tetragon's roughly 30-second event-cache retry budget
// plus transport and fresh Docker inspection.
func assessIdentity(b binding, e Event, info workload.Container, late bool, coverage Coverage, now time.Time) (Assurance, string) {
	if b.identity == nil {
		return ObservationOnly, "admission_identity_unavailable"
	}
	ns := e.Process.Namespaces
	if ns == nil {
		return ObservationOnly, "event_identity_incomplete"
	}
	if ns.PID.Host || ns.Cgroup.Host ||
		(ns.PID.Inum != 0 && ns.PID.Inum != b.pidNS) ||
		(ns.Cgroup.Inum != 0 && ns.Cgroup.Inum != b.cgroupNS) {
		return ConflictingIdentity, "namespace_mismatch"
	}
	if ns.PID.Inum == 0 || ns.Cgroup.Inum == 0 || e.Process.Docker == "" {
		return ObservationOnly, "event_identity_incomplete"
	}
	if _, _, ok := b.identity(info); late || !ok {
		return ObservationOnly, "lifecycle_unverified"
	}
	if coverage.Status != "observing" || coverage.Generation != b.coverage.Generation {
		return ObservationOnly, "coverage_unverified"
	}
	if age := now.Sub(e.Process.Start); age > 45*time.Second || age < -2*time.Second {
		return ObservationOnly, "event_freshness_unverified"
	}
	return VerifiedIdentity, "namespace_binding_matched"
}

// RecheckIdentity validates a previously resolved event against fresh inventory
// and current coverage without adding a second deduplication entry.
func (c *Correlator) RecheckIdentity(e Event, resolved Result, inventory []workload.Container, coverage Coverage) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if resolved.Assurance != VerifiedIdentity || resolved.Status != "correlated" ||
		resolved.ExecID != e.Process.ExecID || e.Process.Docker == "" ||
		!strings.HasPrefix(resolved.ContainerID, e.Process.Docker) ||
		(e.FullID != "" && normalizedFull(e.FullID) != resolved.ContainerID) {
		return false
	}
	b, ok := c.bindings[resolved.ContainerID]
	if !ok || b.job != resolved.JobID || b.invalid || !b.finished.IsZero() ||
		e.Process.Start.Before(b.started) || e.Process.Start.Before(b.latest) ||
		c.seen[e.Node+"\x00exec\x00"+e.Process.ExecID].IsZero() {
		return false
	}
	for id := range c.bindings {
		if id != resolved.ContainerID && strings.HasPrefix(id, e.Process.Docker) {
			return false
		}
	}
	var info workload.Container
	for _, candidate := range inventory {
		if !workload.IDPattern.MatchString(candidate.ID) {
			return false
		}
		if !strings.HasPrefix(candidate.ID, e.Process.Docker) {
			continue
		}
		if candidate.ID != resolved.ContainerID || info.ID != "" {
			return false
		}
		info = candidate
	}
	assurance, _ := assessIdentity(b, e, info, false, coverage, time.Now())
	return assurance == VerifiedIdentity
}
