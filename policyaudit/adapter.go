// Package policyaudit adapts verified observation metadata to pure policy inputs.
// It has no credential, lease client, Docker operation, or enforcement interface.
package policyaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/executionpolicy"
	"github.com/grdamico/runawaybao/runtimeevents"
)

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

var ErrBinding = errors.New("invalid or conflicting policy binding")
var ErrAudit = errors.New("dry-run audit output failed")

type Binding struct{ JobID, WorkloadID, PolicyID, PolicyVersion string }
type bound struct {
	job    string
	policy *executionpolicy.CompiledPolicy
}
type Adapter struct {
	mu       sync.RWMutex
	policies map[string]*executionpolicy.CompiledPolicy
	bindings map[string]bound
}

func New(policies []executionpolicy.Policy) (*Adapter, error) {
	if len(policies) == 0 || len(policies) > 64 {
		return nil, executionpolicy.ErrPolicy
	}
	a := &Adapter{policies: make(map[string]*executionpolicy.CompiledPolicy), bindings: make(map[string]bound)}
	for _, p := range policies {
		c, err := executionpolicy.Compile(p)
		if err != nil {
			return nil, err
		}
		key := p.ID + "\x00" + p.Version
		if _, ok := a.policies[key]; ok {
			return nil, ErrBinding
		}
		a.policies[key] = c
	}
	return a, nil
}

// Bind is a trusted admission operation, called before the associated workload
// starts. There is no update/reload API; job and workload associations are unique.
func (a *Adapter) Bind(b Binding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.policies[b.PolicyID+"\x00"+b.PolicyVersion]
	if p == nil || !executionpolicy.ValidIdentifier(b.JobID) || !containerID.MatchString(b.WorkloadID) || len(a.bindings) >= 64 {
		return ErrBinding
	}
	if _, ok := a.bindings[b.WorkloadID]; ok {
		return ErrBinding
	}
	for _, old := range a.bindings {
		if old.job == b.JobID {
			return ErrBinding
		}
	}
	a.bindings[b.WorkloadID] = bound{b.JobID, p}
	return nil
}
func coverage(c runtimeevents.Coverage) executionpolicy.Coverage {
	return executionpolicy.Coverage{Status: c.Status, Generation: c.Generation, HadGaps: c.Generation > 0}
}

// PolicyDigest reports the digest of a specific trusted immutable binding.
func (a *Adapter) PolicyDigest(job, workload, id, version string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	b, ok := a.bindings[workload]
	if !ok || b.job != job {
		return ""
	}
	policyID, policyVersion := b.policy.Identity()
	if policyID != id || policyVersion != version {
		return ""
	}
	return b.policy.Digest()
}

// Evaluate must receive the original event and result from the trusted correlator,
// never caller-supplied job IDs. Result matching is checked again at this boundary.
func (a *Adapter) Evaluate(e runtimeevents.Event, r runtimeevents.Result, c runtimeevents.Coverage) executionpolicy.Decision {
	in := executionpolicy.Input{Kind: "exec", Binary: e.Process.Binary, Timestamp: e.Process.Start, Coverage: coverage(c), Terminated: r.Terminated, OutOfOrder: r.OutOfOrder}
	switch r.Reason {
	case "missing_container_identity":
		in.CorrelationReason = "missing_identity"
	case "ambiguous_container_identity":
		in.CorrelationReason = "ambiguous_identity"
	case "conflicting_identity":
		in.CorrelationReason = "conflicting_identity"
	case "unknown_container", "unregistered_container", "container_missing":
		in.CorrelationReason = "unknown_workload"
	case "inactive_binding", "container_generation_changed", "outside_workload_lifetime", "lifecycle_unknown":
		in.CorrelationReason = "lifecycle_unverified"
	case "inventory_unavailable", "invalid_inventory":
		in.CorrelationReason = "inventory_unavailable"
	case "missing_execution_identity":
		in.CorrelationReason = "execution_metadata_incomplete"
	case "binding_changed":
		in.CorrelationReason = "binding_mismatch"
	case "dedup_capacity":
		in.CorrelationReason = "dedup_capacity"
	}
	if e.Process.ExecID != "" {
		h := sha256.Sum256([]byte(e.Node + "\x00exec\x00" + e.Process.ExecID))
		in.EventID = hex.EncodeToString(h[:])
	}
	if r.Reason == "duplicate_event" {
		in.Correlation = "duplicate"
	}
	var p *executionpolicy.CompiledPolicy
	// Reject accidentally paired results even when a caller claims correlation.
	// Docker inventory/lifecycle verification remains solely in the correlator.
	identityMatches := containerID.MatchString(r.ContainerID) && (e.Process.Docker != "" || e.FullID != "")
	if e.Process.Docker != "" {
		identityMatches = identityMatches && (len(e.Process.Docker) == 31 || len(e.Process.Docker) == 64) && strings.HasPrefix(r.ContainerID, e.Process.Docker)
	}
	if e.FullID != "" {
		full := e.FullID
		if strings.HasPrefix(full, "docker://") {
			full = strings.TrimPrefix(full, "docker://")
		} else if strings.HasPrefix(full, "containerd://") {
			full = strings.TrimPrefix(full, "containerd://")
		}
		identityMatches = identityMatches && full == r.ContainerID
	}
	if r.Status == "correlated" && r.ExecID == e.Process.ExecID && identityMatches {
		in.Correlation = "verified"
		in.JobID = r.JobID
		in.WorkloadID = r.ContainerID
		in.Method = "full_identity"
		if len(e.Process.Docker) == 31 && e.FullID == "" {
			in.Method = "unique_prefix"
		}
		a.mu.RLock()
		b, ok := a.bindings[r.ContainerID]
		a.mu.RUnlock()
		if ok {
			if b.job != r.JobID {
				in.Correlation = "unverified"
			} else {
				p = b.policy
			}
		}
	}

	return executionpolicy.Evaluate(in, p)
}

// Writer serializes sanitized records only. It never serializes event/result
// structs, which can contain private metadata. Errors never wrap sink diagnostics.
type Writer struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func NewWriter(w io.Writer) *Writer {
	result := &Writer{}
	if w != nil {
		result.encoder = json.NewEncoder(w)
	}
	return result
}
func (w *Writer) Decision(d executionpolicy.Decision) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.encoder == nil || w.encoder.Encode(d) != nil {
		return ErrAudit
	}
	return nil
}
func (w *Writer) Coverage(c runtimeevents.Coverage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	record := struct {
		Mode     string                   `json:"mode"`
		Type     string                   `json:"type"`
		Coverage executionpolicy.Coverage `json:"coverage"`
	}{"dry_run", "observation_coverage", coverage(c)}
	if w.encoder == nil || w.encoder.Encode(record) != nil {
		return ErrAudit
	}
	return nil
}

// Gap preserves a delivered gap's own timestamp/status context. If its notice is
// lost, periodic Coverage records still expose the monotonically increasing count.
func (w *Writer) Gap(g runtimeevents.Gap) error {
	reason := "observation_gap"
	switch g.Reason {
	case "stream_interrupted", "queue_overflow", "malformed_frame", "oversized_frame", "source_reported_loss":
		reason = g.Reason
	}
	record := struct {
		Mode       string    `json:"mode"`
		Type       string    `json:"type"`
		Reason     string    `json:"reason"`
		Timestamp  time.Time `json:"timestamp"`
		Generation uint64    `json:"generation"`
	}{"dry_run", "observation_gap", reason, g.At, g.Generation}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.encoder == nil || w.encoder.Encode(record) != nil {
		return ErrAudit
	}
	return nil
}
