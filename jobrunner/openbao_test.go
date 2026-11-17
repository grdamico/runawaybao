package jobrunner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/grdamico/runawaybao/jobrunner"
	"github.com/grdamico/runawaybao/openbao"
)

func TestRunnerWithOpenBaoClient(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/secret/data/demo" || r.Header.Get("X-Vault-Token") != "fixture-token" {
			t.Error("incorrect client request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"username":"fixture-user","password":"fixture-password"}}}`))
	}))
	defer server.Close()
	client, err := openbao.NewClient(openbao.Config{Address: server.URL, Token: "fixture-token"})
	if err != nil {
		t.Fatal("client configuration failed")
	}
	r := jobrunner.NewRunnerWithReader(jobrunner.NewPolicy(jobrunner.PermissionReadDemo), client)
	job := jobrunner.Job{ID: "read", Action: jobrunner.ActionReadDemo}
	if got := r.RunContext(context.Background(), job); got != (jobrunner.JobResult{JobID: "read", Status: jobrunner.Succeeded}) {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got := r.RunContext(context.Background(), job); got.Error != "duplicate job ID" {
		t.Fatal("duplicate accepted")
	}
	denied := jobrunner.NewRunnerWithReader(jobrunner.NewPolicy(), client)
	if got := denied.RunContext(context.Background(), job); got.Status != jobrunner.Denied {
		t.Fatal("unauthorized read accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected secret requests")
	}
}
