package policyaudit

import (
	"bytes"
	"errors"
	"github.com/grdamico/runawaybao/executionpolicy"
	"github.com/grdamico/runawaybao/runtimeevents"
	"strings"
	"testing"
	"time"
)

func fixture() (runtimeevents.Event, runtimeevents.Result) {
	id := strings.Repeat("a", 64)
	return runtimeevents.Event{Process: runtimeevents.Process{Docker: id[:31], ExecID: "opaque-exec", Binary: "/bin/sh", Start: time.Unix(42, 0)}}, runtimeevents.Result{Status: "correlated", JobID: "A", ContainerID: id, ExecID: "opaque-exec", LeaseID: "sensitive-lease-id"}
}
func TestPolicyIsolationBindingsAndRedaction(t *testing.T) {
	a, err := New(executionpolicy.DemoPolicies())
	if err != nil {
		t.Fatal(err)
	}
	e, r := fixture()
	bind := Binding{"A", r.ContainerID, "demo-shell", "v1"}
	if a.Bind(bind) != nil {
		t.Fatal("bind")
	}
	if a.Bind(bind) != ErrBinding {
		t.Fatal("rebind allowed")
	}
	other := strings.Repeat("b", 64)
	if a.Bind(Binding{"B", other, "demo-no-shell", "v1"}) != nil {
		t.Fatal("B bind")
	}
	c := runtimeevents.Coverage{Status: "observing"}
	dA := a.Evaluate(e, r, c)
	if dA.Outcome != executionpolicy.Allowed {
		t.Fatal("A")
	}
	r.JobID = "B"
	r.ContainerID = other
	e.Process.Docker = other[:31]
	dB := a.Evaluate(e, r, c)
	if dB.Outcome != executionpolicy.Suspicious || dB.PolicyID != "demo-no-shell" || dA.PolicyID != "demo-shell" {
		t.Fatal("policy crossed jobs")
	}
	var output bytes.Buffer
	w := NewWriter(&output)
	if w.Decision(dB) != nil {
		t.Fatal("write")
	}
	if strings.Contains(output.String(), "sensitive-lease") || strings.Contains(output.String(), "opaque-exec") || strings.Contains(output.String(), "/bin/sh") {
		t.Fatal("private event metadata leaked")
	}
	r.Status = "uncorrelated"
	r.Reason = "ambiguous_container_identity"
	d := a.Evaluate(e, r, c)
	if d.Recommendation != "" || d.JobID != "" || d.Outcome != executionpolicy.Unknown {
		t.Fatal("ambiguous targeted")
	}
	r.Status = "correlated"
	r.JobID = "wrong-job"
	if a.Evaluate(e, r, c).Recommendation != "" {
		t.Fatal("binding conflict targeted")
	}
}
func TestMissingAndInvalidPolicy(t *testing.T) {
	if _, err := New([]executionpolicy.Policy{{}}); err == nil {
		t.Fatal("invalid policy admitted")
	}
	a, _ := New(executionpolicy.DemoPolicies())
	e, r := fixture()
	d := a.Evaluate(e, r, runtimeevents.Coverage{Status: "observing"})
	if d.Reason != "policy_unavailable" || d.Correlation != "verified" || d.Recommendation != "" {
		t.Fatal("missing policy conflated with correlation")
	}
	if a.Bind(Binding{"A", r.ContainerID, "unknown", "v1"}) == nil {
		t.Fatal("missing profile admitted")
	}
	p := executionpolicy.DemoPolicies()
	p = append(p, p[0])
	if _, err := New(p); err != ErrBinding {
		t.Fatal("duplicate version admitted")
	}
}

type failingSink struct{}

func (failingSink) Write([]byte) (int, error) { return 0, errors.New("sensitive-password") }
func TestAuditFailureSanitized(t *testing.T) {
	w := NewWriter(failingSink{})
	if w.Decision(executionpolicy.Decision{}) != ErrAudit || w.Coverage(runtimeevents.Coverage{}) != ErrAudit {
		t.Fatal("raw audit failure returned")
	}
}

func TestCorrelationReasonsAndLifecycle(t *testing.T) {
	a, _ := New(executionpolicy.DemoPolicies())
	e, r := fixture()
	_ = a.Bind(Binding{"A", r.ContainerID, "demo-no-shell", "v1"})
	c := runtimeevents.Coverage{Status: "observing", Generation: 3}
	r.Terminated = true
	d := a.Evaluate(e, r, c)
	if d.Outcome != executionpolicy.Unknown || d.Recommendation != "" || !d.Terminated {
		t.Fatal("historical event recommended response")
	}
	r.Terminated = false
	r.OutOfOrder = true
	d = a.Evaluate(e, r, c)
	if d.Outcome != executionpolicy.Suspicious || !d.OutOfOrder || !d.Coverage.HadGaps {
		t.Fatal("active out-of-order point evaluation lost immutable policy or gap history")
	}
	r.Status = "uncorrelated"
	r.Reason = "ambiguous_container_identity"
	d = a.Evaluate(e, r, c)
	if d.CorrelationReason != "ambiguous_identity" || d.JobID != "" {
		t.Fatal("uncertainty conflated with policy result")
	}
}
func TestGapReasonSanitized(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	if w.Gap(runtimeevents.Gap{Reason: "sensitive-password", At: time.Unix(42, 0), Generation: 2}) != nil {
		t.Fatal("gap write")
	}
	if strings.Contains(b.String(), "sensitive-password") {
		t.Fatal("raw gap reason escaped")
	}
	if NewWriter(failingSink{}).Gap(runtimeevents.Gap{}) != ErrAudit {
		t.Fatal("raw gap write error escaped")
	}
}

func TestMissingOrConflictingEventCannotBorrowCorrelatedResult(t *testing.T) {
	for _, kind := range []string{"missing", "conflicting", "wrong-exec"} {
		a, _ := New(executionpolicy.DemoPolicies())
		e, r := fixture()
		_ = a.Bind(Binding{"A", r.ContainerID, "demo-no-shell", "v1"})
		switch kind {
		case "missing":
			e.Process.Docker = ""
		case "conflicting":
			e.FullID = strings.Repeat("b", 64)
		case "wrong-exec":
			e.Process.ExecID = "different"
		}
		d := a.Evaluate(e, r, runtimeevents.Coverage{Status: "observing"})
		if d.Outcome != executionpolicy.Unknown || d.Recommendation != "" || d.JobID != "" {
			t.Fatal("uncertain event borrowed another result's target")
		}
	}
}

func TestMissingAuditSinkFailsClosed(t *testing.T) {
	w := NewWriter(nil)
	if w.Decision(executionpolicy.Decision{}) != ErrAudit || w.Coverage(runtimeevents.Coverage{}) != ErrAudit || w.Gap(runtimeevents.Gap{}) != ErrAudit {
		t.Fatal("missing sink was ignored")
	}
}
