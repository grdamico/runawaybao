package executionpolicy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func input() Input {
	return Input{Kind: "exec", JobID: "A", WorkloadID: strings.Repeat("a", 64), EventID: strings.Repeat("b", 64), Binary: "/bin/sleep", Correlation: "verified", Method: "unique_prefix", Timestamp: time.Unix(42, 0).UTC(), Coverage: Coverage{Status: "observing"}}
}
func compiled(t *testing.T) *CompiledPolicy {
	t.Helper()
	p, e := Compile(Policy{"safe", "v1", []string{"/bin/sleep"}})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestOutcomesAndUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         func(*Input)
		nilPolicy      bool
		out            Outcome
		reason, action string
	}{
		{"allowed", func(*Input) {}, false, Allowed, "executable_allowed", ""},
		{"unexpected", func(i *Input) { i.Binary = "/bin/sh" }, false, Suspicious, "executable_not_allowed", "would_revoke_lease"},
		{"ambiguous", func(i *Input) { i.Correlation = "unverified"; i.Binary = "/bin/sh" }, false, Unknown, "correlation_unverified", ""},
		{"missing policy", func(*Input) {}, true, Unknown, "policy_unavailable", ""},
		{"missing metadata", func(i *Input) { i.Binary = "" }, false, Unknown, "execution_metadata_incomplete", ""},
		{"unclean path", func(i *Input) { i.Binary = "/bin/../bin/sleep" }, false, Unknown, "execution_metadata_incomplete", ""},
		{"unstable descriptor", func(i *Input) { i.Binary = "/proc/self/fd/6" }, false, Unknown, "unstable_executable_reference", ""},
		{"basename", func(i *Input) { i.Binary = "sleep" }, false, Unknown, "execution_metadata_incomplete", ""},
		{"gap", func(i *Input) { i.Coverage.Status = "interrupted"; i.Coverage.Generation = 2 }, false, Unknown, "coverage_unavailable", ""},
		{"late terminated", func(i *Input) { i.Terminated = true; i.Binary = "/bin/sh" }, false, Unknown, "terminated_workload", ""},
		{"active out of order", func(i *Input) { i.OutOfOrder = true }, false, Allowed, "executable_allowed", ""},
		{"duplicate", func(i *Input) { i.Correlation = "duplicate"; i.Binary = "/bin/sh" }, false, NotApplicable, "duplicate_event", ""},
		{"unsupported", func(i *Input) { i.Kind = "exit" }, false, NotApplicable, "unsupported_event_type", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := input()
			tc.change(&i)
			p := compiled(t)
			if tc.nilPolicy {
				p = nil
			}
			d := Evaluate(i, p)
			if d.Outcome != tc.out || d.Reason != tc.reason || d.Recommendation != tc.action {
				t.Fatalf("unexpected safe decision: %+v", d)
			}
			if i.Correlation != "verified" && (d.JobID != "" || d.WorkloadID != "") {
				t.Fatal("uncertain input produced target")
			}
		})
	}
}
func TestCompileAndImmutableDeterminism(t *testing.T) {
	for _, p := range []Policy{{}, {"p", "", []string{"/bin/sleep"}}, {"p", "v1", nil}, {"p", "v1", []string{"sleep"}}, {"p", "v1", []string{"/proc/self/fd/6"}}, {"p", "v1", []string{"/bin/./sleep"}}, {"p", "v1", []string{"/bin/sleep", "/bin/sleep"}}, {"p", "v1", []string{"/bin/sleep\n"}}} {
		if _, err := Compile(p); err != ErrPolicy {
			t.Fatal("invalid policy accepted")
		}
	}
	paths := []string{"/bin/sleep", "/bin/echo"}
	p, _ := Compile(Policy{"p", "v1", paths})
	paths[0] = "/bin/sh"
	first := Evaluate(input(), p)
	second := Evaluate(input(), p)
	if first.Outcome != Allowed || !reflect.DeepEqual(first, second) {
		t.Fatal("mutable or nondeterministic policy")
	}
	reordered, _ := Compile(Policy{"p", "v1", []string{"/bin/echo", "/bin/sleep"}})
	if !reflect.DeepEqual(first, Evaluate(input(), reordered)) {
		t.Fatal("configuration order affected decision")
	}
}
func TestCoverageHistoryAndOutput(t *testing.T) {
	i := input()
	i.Coverage.Generation = 4
	i.Binary = "/bin/sh"
	d := Evaluate(i, compiled(t))
	if d.Outcome != Suspicious || !d.Coverage.HadGaps {
		t.Fatal("resumed event lost gap history")
	}
	b, _ := json.Marshal(d)
	for _, field := range []string{"password", "token", "arguments", "environment", "lease_id", "connection_string", "/bin/sh"} {
		if strings.Contains(string(b), field) {
			t.Fatal("unsafe audit field")
		}
	}
}

func TestUntrustedReasonIsNotEchoed(t *testing.T) {
	i := input()
	i.Correlation = "unverified"
	i.CorrelationReason = "sensitive-token"
	d := Evaluate(i, compiled(t))
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "sensitive-token") || d.JobID != "" || d.Recommendation != "" {
		t.Fatal("untrusted reason or target leaked")
	}
}

func TestZeroCompiledPolicyFailsClosed(t *testing.T) {
	d := Evaluate(input(), new(CompiledPolicy))
	if d.Outcome != Unknown || d.Reason != "policy_invalid" || d.Recommendation != "" {
		t.Fatal("uncompiled zero policy produced an actionable finding")
	}
}

func TestProviderNeutralWorkloadIdentity(t *testing.T) {
	i := input()
	i.WorkloadID = "worker-execution-1"
	i.Method = "full_identity"
	if Evaluate(i, compiled(t)).Outcome != Allowed {
		t.Fatal("pure evaluator assumes a specific container runtime identity format")
	}
}
