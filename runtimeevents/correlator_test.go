package runtimeevents

import (
	"strings"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/workload"
)

func setup(t *testing.T) (*Correlator, Event, []workload.Container) {
	t.Helper()
	id := strings.Repeat("a", 64)
	start := time.Now().Add(-time.Minute)
	c := NewCorrelator(func(job string) (Metadata, bool) { return Metadata{job, id, "lease-A"}, job == "A" })
	if c.Register(id, "A") != nil {
		t.Fatal("register")
	}
	info := workload.Container{ID: id, StartedAt: start, Running: true}
	if c.Activate(info) != nil {
		t.Fatal("activate")
	}
	return c, Event{Process: Process{Docker: id[:31], ExecID: "unique", PID: 12, Start: start.Add(time.Second)}, Time: start.Add(2 * time.Second)}, []workload.Container{info}
}
func TestCorrelationConservative(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Event, *[]workload.Container)
		reason string
	}{
		{"prefix", func(*Event, *[]workload.Container) {}, ""},
		{"missing", func(e *Event, _ *[]workload.Container) { e.Process.Docker = "" }, "missing_container_identity"},
		{"unsupported short ID", func(e *Event, _ *[]workload.Container) { e.Process.Docker = e.Process.Docker[:12] }, "unsupported_container_identity"},
		{"unknown", func(e *Event, _ *[]workload.Container) { e.Process.Docker = strings.Repeat("b", 31) }, "unknown_container"},
		{"conflict", func(e *Event, _ *[]workload.Container) { e.FullID = strings.Repeat("b", 64) }, "conflicting_identity"},
		{"ambiguous", func(e *Event, i *[]workload.Container) {
			*i = append(*i, workload.Container{ID: e.Process.Docker + strings.Repeat("b", 33)})
		}, "ambiguous_container_identity"},
		{"PID without execution ID", func(e *Event, _ *[]workload.Container) { e.Process.ExecID = "" }, "missing_execution_identity"},
		{"before container start", func(e *Event, i *[]workload.Container) { e.Process.Start = (*i)[0].StartedAt.Add(-time.Second) }, "outside_workload_lifetime"},
		{"restart", func(_ *Event, i *[]workload.Container) { (*i)[0].Restarts = 1 }, "container_generation_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e, i := setup(t)
			tc.change(&e, &i)
			r := c.Resolve(e, i, true)
			if r.Reason != tc.reason {
				t.Fatalf("reason %q want %q", r.Reason, tc.reason)
			}
			if tc.reason != "" && (r.JobID != "" || r.LeaseID != "") {
				t.Fatal("unverified identity attributed")
			}
			if tc.reason == "" && (r.JobID != "A" || r.LeaseID != "lease-A") {
				t.Fatal("incorrect mapping")
			}
		})
	}
}
func TestChildrenDuplicatesPIDReuseAndLate(t *testing.T) {
	c, e, i := setup(t)
	if c.Resolve(e, i, true).Status != "correlated" {
		t.Fatal("parent")
	}
	child := e
	child.Process.ExecID = "child"
	child.Process.ParentExecID = e.Process.ExecID
	child.Process.PID = 13
	if r := c.Resolve(child, i, true); r.Status != "correlated" || r.ParentExecID != "unique" {
		t.Fatal("child")
	}
	if c.Resolve(child, i, true).Reason != "duplicate_event" {
		t.Fatal("duplicate")
	}
	child.Process.ExecID = "reused-pid"
	child.Process.Start = child.Process.Start.Add(time.Second)
	if c.Resolve(child, i, true).Status != "correlated" {
		t.Fatal("PID reuse was confused with execution identity")
	}
	info := i[0]
	info.Running = false
	info.FinishedAt = time.Now().Add(-time.Second)
	if c.Retire(info) != nil {
		t.Fatal("retire")
	}
	e.Process.ExecID = "late"
	if r := c.Resolve(e, nil, true); r.Status != "correlated" || !r.Late {
		t.Fatal("late event lost")
	}
	e.Process.ExecID = "after-exit"
	e.Time = time.Now()
	e.Process.Start = time.Now()
	if c.Resolve(e, nil, true).Reason != "outside_workload_lifetime" {
		t.Fatal("event outside lifetime attributed")
	}
}
func TestBoundsAndInventoryFailure(t *testing.T) {
	c, e, i := setup(t)
	if c.Resolve(e, i, false).Reason != "inventory_unavailable" {
		t.Fatal("inventory failure guessed")
	}
	for n := 0; n < 4096; n++ {
		c.seen[string(rune(n))] = time.Now()
	}
	if c.Resolve(e, i, true).Reason != "dedup_capacity" {
		t.Fatal("capacity ignored")
	}
}

func TestParentCannotSupplyWorkloadIdentity(t *testing.T) {
	c, e, inventory := setup(t)
	if c.Resolve(e, inventory, true).Status != "correlated" {
		t.Fatal("parent")
	}
	e.Process.ExecID = "child-without-container"
	e.Process.ParentExecID = "unique"
	e.Process.Docker = ""
	if r := c.Resolve(e, inventory, true); r.Reason != "missing_container_identity" || r.JobID != "" {
		t.Fatal("parent identity guessed for child")
	}
}
func TestBindingAndRestartRules(t *testing.T) {
	c, e, inventory := setup(t)
	if !c.NeedsInventory(e) {
		t.Fatal("potential match skipped verification")
	}
	unknown := e
	unknown.Process.Docker = strings.Repeat("b", 31)
	if c.NeedsInventory(unknown) {
		t.Fatal("unrelated event requires inventory")
	}
	inventory[0].StartedAt = inventory[0].StartedAt.Add(time.Second)
	if c.Resolve(e, inventory, true).Reason != "container_generation_changed" {
		t.Fatal("same-ID restart attributed")
	}
	inventory[0].StartedAt = inventory[0].StartedAt.Add(-time.Second)
	if c.Resolve(e, inventory, true).Reason != "inactive_binding" {
		t.Fatal("invalidated generation resurrected")
	}
}

func TestEnvelopeClockDoesNotOverrideExecutionClock(t *testing.T) {
	c, e, i := setup(t)
	e.Time = e.Process.Start.Add(-2 * time.Second)
	if c.Resolve(e, i, true).Status != "correlated" {
		t.Fatal("envelope clock skew rejected verified execution")
	}
}

func TestOutOfOrderExecutionIsMarkedLate(t *testing.T) {
	c, e, i := setup(t)
	newer := e
	newer.Process.ExecID = "newer"
	newer.Process.Start = e.Process.Start.Add(time.Second)
	if c.Resolve(newer, i, true).Status != "correlated" {
		t.Fatal("newer")
	}
	if result := c.Resolve(e, i, true); result.Status != "correlated" || !result.Late {
		t.Fatal("out-of-order execution not marked late")
	}
}

func TestTerminationAndOutOfOrderAreDistinct(t *testing.T) {
	c, e, i := setup(t)
	later := e
	later.Process.ExecID = "newer-event"
	later.Process.Start = e.Process.Start.Add(time.Second)
	_ = c.Resolve(later, i, true)
	r := c.Resolve(e, i, true)
	if !r.OutOfOrder || r.Terminated {
		t.Fatal("active late event treated as terminated")
	}
	info := i[0]
	info.Running = false
	info.FinishedAt = time.Now()
	if c.Retire(info) != nil {
		t.Fatal("retire")
	}
	e.Process.ExecID = "historical"
	r = c.Resolve(e, nil, true)
	if !r.Terminated {
		t.Fatal("terminated event not marked historical")
	}
}
