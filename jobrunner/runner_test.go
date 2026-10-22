package jobrunner

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func TestPolicy(t *testing.T) {
	grants := []string{PermissionExecute, ""}
	p := NewPolicy(grants...)
	grants[0] = "changed"
	for _, tc := range []struct {
		permission string
		want       bool
	}{{PermissionExecute, true}, {"changed", false}, {"missing", false}, {"", false}, {" ", false}} {
		if got := p.Allows(tc.permission); got != tc.want {
			t.Errorf("Allows(%q) = %v, want %v", tc.permission, got, tc.want)
		}
	}
	if (Policy{}).Allows(PermissionExecute) {
		t.Fatal("zero policy must deny")
	}
}

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		grant  bool
		status Status
		err    string
	}{
		{"authorized success", "succeed", true, Succeeded, ""},
		{"unauthorized success", "succeed", false, Denied, "permission denied"},
		{"denied before simulated failure", "fail", false, Denied, "permission denied"},
		{"unknown action without grants", "unknown", false, Failed, "unknown action"},
		{"unknown action", "unknown", true, Failed, "unknown action"},
		{"execution failure", "fail", true, Failed, "simulated execution failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := NewPolicy()
			if tc.grant {
				policy = NewPolicy(PermissionExecute)
			}
			got := NewRunner(policy).Run(Job{"job-1", tc.action})
			want := JobResult{"job-1", tc.status, tc.err}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestInvalidJobs(t *testing.T) {
	for _, job := range []Job{
		{},
		{"", "succeed"},
		{" \t", "succeed"},
		{"id", ""},
		{"id", "\n"},
	} {
		r := NewRunner(NewPolicy(PermissionExecute, "weaker-grant"))
		got := r.Run(job)
		if got.JobID != job.ID || got.Status != Failed || got.Error == "" {
			t.Errorf("Run(%+v) = %+v, want failed validation", job, got)
		}
	}
}

func TestInvalidRequestDoesNotReserveID(t *testing.T) {
	for _, invalidAction := range []string{"", " \t\n"} {
		r := NewRunner(NewPolicy(PermissionExecute))
		if got := r.Run(Job{"id", invalidAction}); got.Status != Failed {
			t.Fatalf("invalid request = %+v", got)
		}
		if got := r.Run(Job{"id", "succeed"}); got.Status != Succeeded {
			t.Fatalf("corrected request = %+v", got)
		}
	}
}

func TestDuplicateIDs(t *testing.T) {
	for _, action := range []string{"succeed", "fail", "unknown"} {
		t.Run(action, func(t *testing.T) {
			r := NewRunner(NewPolicy(PermissionExecute))
			job := Job{"duplicate", action}
			r.Run(job)
			job.Action = "succeed"
			if got := r.Run(job); got != (JobResult{"duplicate", Failed, "duplicate job ID"}) {
				t.Fatalf("duplicate = %+v", got)
			}
			job.ID = "distinct"
			if got := r.Run(job); got.Status != Succeeded {
				t.Fatalf("distinct ID = %+v", got)
			}
		})
	}
	var r Runner
	job := Job{"denied", "succeed"}
	if got := r.Run(job); got.Status != Denied {
		t.Fatalf("zero runner = %+v", got)
	}
	if got := r.Run(job); got.Error != "duplicate job ID" {
		t.Fatalf("denied ID was not reserved: %+v", got)
	}
}

func TestConcurrentDuplicateIDs(t *testing.T) {
	r := NewRunner(NewPolicy(PermissionExecute))
	results := make(chan JobResult, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- r.Run(Job{"same", "succeed"})
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.Status == Succeeded {
			successes++
		} else if result.Status != Failed || result.Error != "duplicate job ID" {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful executions = %d, want 1", successes)
	}
}

func TestResultJSON(t *testing.T) {
	for _, status := range []Status{Succeeded, Failed, Denied} {
		result := JobResult{JobID: "audit-job", Status: status}
		if status != Succeeded {
			result.Error = "reason"
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		_, hasError := fields["error"]
		if fields["job_id"] != result.JobID || fields["status"] != string(status) || hasError != (status != Succeeded) {
			t.Fatalf("unexpected audit JSON: %s", encoded)
		}
	}
}

// Unknown JSON fields cannot alter action metadata. A future HTTP layer may
// additionally choose to reject them using Decoder.DisallowUnknownFields.
func TestJobCannotSpecifyPermission(t *testing.T) {
	if reflect.TypeOf(Job{}).NumField() != 2 {
		t.Fatal("job request must contain only ID and Action")
	}
	for _, actionName := range []string{"succeed", "fail"} {
		var job Job
		input := `{"id":"injected","action":"` + actionName + `","required_permission":"weaker-grant"}`
		if err := json.Unmarshal([]byte(input), &job); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || fields["id"] != job.ID || fields["action"] != actionName {
			t.Fatalf("unexpected request JSON: %s", encoded)
		}
		r := NewRunner(NewPolicy("weaker-grant"))
		if got := r.Run(job); got.Status != Denied || got.Error != "permission denied" {
			t.Fatalf("injected permission changed authorization: %+v", got)
		}
	}
}
