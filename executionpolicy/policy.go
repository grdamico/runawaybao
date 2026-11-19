// Package executionpolicy evaluates immutable executable allowlists. It performs
// no I/O, reads no clocks, and accepts no transport or enforcement interfaces.
package executionpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrPolicy = errors.New("invalid execution policy")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var controlPath = regexp.MustCompile(`[\x00-\x1f\x7f]`)
var eventReference = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidIdentifier(s string) bool { return identifier.MatchString(s) }
func ValidWorkload(s string) bool   { return ValidIdentifier(s) }

type Policy struct {
	ID, Version        string
	AllowedExecutables []string
}
type CompiledPolicy struct {
	id, version string
	paths       map[string]bool
}

func canonical(s string) bool {
	return len(s) > 1 && len(s) <= 4096 && path.IsAbs(s) && path.Clean(s) == s && !controlPath.MatchString(s)
}
func unstableReference(s string) bool {
	return s == "/proc" || strings.HasPrefix(s, "/proc/") || s == "/dev/fd" || strings.HasPrefix(s, "/dev/fd/")
}

func Compile(p Policy) (*CompiledPolicy, error) {
	if !ValidIdentifier(p.ID) || !ValidIdentifier(p.Version) || len(p.AllowedExecutables) == 0 || len(p.AllowedExecutables) > 64 {
		return nil, ErrPolicy
	}
	c := &CompiledPolicy{id: p.ID, version: p.Version, paths: make(map[string]bool)}
	for _, name := range p.AllowedExecutables {
		if !canonical(name) || unstableReference(name) || c.paths[name] {
			return nil, ErrPolicy
		}
		c.paths[name] = true
	}
	return c, nil
}
func (p *CompiledPolicy) Identity() (string, string) {
	if p == nil {
		return "", ""
	}
	return p.id, p.version
}

// Digest identifies the validated policy independently of allowlist ordering.
func (p *CompiledPolicy) Digest() string {
	if p == nil {
		return ""
	}
	paths := make([]string, 0, len(p.paths))
	for name := range p.paths {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	sum := sha256.Sum256([]byte(p.id + "\x00" + p.version + "\x00" + strings.Join(paths, "\x00")))
	return hex.EncodeToString(sum[:])
}

type Outcome string

const (
	Allowed       Outcome = "allowed"
	Suspicious    Outcome = "suspicious"
	Unknown       Outcome = "unknown"
	NotApplicable Outcome = "not_applicable"
)

type Coverage struct {
	Status     string `json:"status"`
	Generation uint64 `json:"generation"`
	HadGaps    bool   `json:"had_gaps"`
}
type Input struct {
	Kind, JobID, WorkloadID, EventID, Binary, Correlation, Method string
	CorrelationReason                                             string
	Timestamp                                                     time.Time
	Coverage                                                      Coverage
	Terminated, OutOfOrder                                        bool
}

// Decision has no field for executable payloads, arguments, credentials or leases.
// Recommendation is text for audit only, not an executable action.
type Decision struct {
	CorrelationReason string    `json:"correlation_reason"`
	Mode              string    `json:"mode"`
	JobID             string    `json:"job_id,omitempty"`
	WorkloadID        string    `json:"workload_id,omitempty"`
	PolicyID          string    `json:"policy_id,omitempty"`
	PolicyVersion     string    `json:"policy_version,omitempty"`
	EventID           string    `json:"event_id,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	Correlation       string    `json:"correlation"`
	Method            string    `json:"correlation_method,omitempty"`
	Outcome           Outcome   `json:"outcome"`
	Rule              string    `json:"rule"`
	Reason            string    `json:"reason"`
	Coverage          Coverage  `json:"coverage"`
	Terminated        bool      `json:"terminated"`
	OutOfOrder        bool      `json:"out_of_order"`
	Recommendation    string    `json:"recommendation,omitempty"`
}

func Evaluate(in Input, p *CompiledPolicy) Decision {
	coverage := in.Coverage
	if coverage.Status != "observing" && coverage.Status != "interrupted" {
		coverage.Status = "unknown"
	}
	coverage.HadGaps = coverage.HadGaps || coverage.Generation > 0
	d := Decision{Mode: "dry_run", Timestamp: in.Timestamp, Correlation: "unverified", Outcome: Unknown, Rule: "exec.allowlist", Coverage: coverage, Terminated: in.Terminated, OutOfOrder: in.OutOfOrder}
	d.CorrelationReason = "unverified"
	switch in.CorrelationReason {
	case "missing_identity", "ambiguous_identity", "conflicting_identity", "unknown_workload", "lifecycle_unverified", "inventory_unavailable", "execution_metadata_incomplete", "binding_mismatch", "dedup_capacity":
		d.CorrelationReason = in.CorrelationReason
	}
	if eventReference.MatchString(in.EventID) {
		d.EventID = in.EventID
	}
	if in.Kind != "exec" {
		d.Outcome = NotApplicable
		d.Reason = "unsupported_event_type"
		return d
	}
	if in.Correlation == "duplicate" {
		d.Outcome = NotApplicable
		d.Correlation = "duplicate"
		d.CorrelationReason = "duplicate_event"
		d.Reason = "duplicate_event"
		return d
	}
	if in.Correlation != "verified" || !ValidIdentifier(in.JobID) || !ValidWorkload(in.WorkloadID) || (in.Method != "full_identity" && in.Method != "unique_prefix") {
		d.Reason = "correlation_unverified"
		return d
	}
	d.Correlation = "verified"
	d.CorrelationReason = "verified_identity"
	d.Method = in.Method
	d.JobID = in.JobID
	d.WorkloadID = in.WorkloadID
	if p == nil {
		d.Reason = "policy_unavailable"
		return d
	}
	if !ValidIdentifier(p.id) || !ValidIdentifier(p.version) || len(p.paths) == 0 {
		d.Reason = "policy_invalid"
		return d
	}
	d.PolicyID = p.id
	d.PolicyVersion = p.version
	if d.EventID == "" || in.Timestamp.IsZero() || !canonical(in.Binary) {
		d.Reason = "execution_metadata_incomplete"
		return d
	}
	if unstableReference(in.Binary) {
		d.Reason = "unstable_executable_reference"
		return d
	}
	if in.Terminated {
		d.Reason = "terminated_workload"
		return d
	}
	if coverage.Status != "observing" {
		d.Reason = "coverage_unavailable"
		return d
	}
	if p.paths[in.Binary] {
		d.Outcome = Allowed
		d.Reason = "executable_allowed"
		return d
	}
	d.Outcome = Suspicious
	d.Reason = "executable_not_allowed"
	d.Recommendation = "would_revoke_lease"
	return d
}

// DemoPolicies are explicit harmless lab profiles, not generic threat rules.
func DemoPolicies() []Policy {
	return []Policy{{ID: "demo-shell", Version: "v1", AllowedExecutables: []string{"/usr/bin/tail", "/bin/sh", "/bin/sleep"}}, {ID: "demo-no-shell", Version: "v1", AllowedExecutables: []string{"/usr/bin/tail", "/bin/sleep"}}}
}
