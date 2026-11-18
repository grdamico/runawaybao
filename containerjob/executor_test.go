package containerjob_test

import (
	"context"
	"fmt"
	"github.com/grdamico/runawaybao/containerjob"
	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/jobrunner"
	"github.com/grdamico/runawaybao/openbao"
	"github.com/grdamico/runawaybao/policyaudit"
	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type issuer struct{ calls atomic.Int32 }

func (i *issuer) Issue(context.Context) (openbao.DatabaseCredentials, error) {
	return openbao.DatabaseCredentials{LeaseID: fmt.Sprintf("private-lease-%d", i.calls.Add(1)), Username: "sensitive-user", Password: "sensitive-password", TTL: time.Minute}, nil
}
func TestAuthorizationAndFailedCreateRetainsIndependentMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	if os.WriteFile(path, []byte("#!/bin/sh\necho sensitive-password >&2\nexit 1\n"), 0700) != nil {
		t.Fatal("fixture")
	}
	t.Setenv("PATH", dir)
	issue := &issuer{}
	m, _ := databasejob.NewForWorkloads(issue)
	d, _ := workload.NewDatabase(strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64))
	c := runtimeevents.NewCorrelator(func(id string) (runtimeevents.Metadata, bool) {
		r, ok := m.Lookup(id)
		return runtimeevents.Metadata{JobID: r.JobID, WorkloadID: r.WorkloadID, LeaseID: r.LeaseID}, ok
	})
	a, _ := policyaudit.New(containerjob.Policies())
	e, err := containerjob.New(m, d, c, a)
	if err != nil {
		t.Fatal(err)
	}
	denied := jobrunner.NewRunnerWithDatabase(jobrunner.NewPolicy(), nil, e)
	if r := denied.RunContext(context.Background(), jobrunner.Job{ID: "denied", Action: jobrunner.ActionDBReadDemo}); r.Status != jobrunner.Denied || issue.calls.Load() != 0 {
		t.Fatal("authorization did not precede issuance")
	}
	runner := jobrunner.NewRunnerWithDatabase(jobrunner.NewPolicy(jobrunner.PermissionDBReadDemo), nil, e)
	for _, id := range []string{"A", "B", "A"} {
		r := runner.RunContext(context.Background(), jobrunner.Job{ID: id, Action: jobrunner.ActionDBReadDemo})
		if r.Status != jobrunner.Failed || strings.Contains(r.Error, "sensitive") {
			t.Fatal("failure not sanitized")
		}
	}
	ar, _ := m.Lookup("A")
	br, _ := m.Lookup("B")
	if issue.calls.Load() != 2 || ar.LeaseID == "" || ar.LeaseID == br.LeaseID || ar.State != databasejob.Active || br.State != databasejob.Active {
		t.Fatal("issued leases lost, retried, or cross-corrupted")
	}
	for _, id := range []string{"A", "B"} {
		r, ok := d.Lookup(id)
		if !ok || r.NetworkName == "" || r.Phase != "network_create_pending" {
			t.Fatal("cleanup journal missing")
		}
		if e.Cleanup(context.Background(), id) == nil {
			t.Fatal("failed discovery accepted")
		}
	}
	if os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700) != nil {
		t.Fatal("fixture")
	}
	for _, id := range []string{"A", "B"} {
		if e.Cleanup(context.Background(), id) != nil || e.Cleanup(context.Background(), id) != nil {
			t.Fatal("cleanup retry failed")
		}
		r, _ := m.Lookup(id)
		if r.State != databasejob.Active {
			t.Fatal("resource cleanup changed lease state")
		}
	}
}
