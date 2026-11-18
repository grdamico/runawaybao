package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunIsExplicitAndInvalidBindingFailsBeforeInspection(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()
	file := filepath.Join(t.TempDir(), "bindings.json")
	body := `[{"job_id":"A","workload_id":"` + strings.Repeat("a", 64) + `","lease_id":"private-only","started_at":"2026-01-01T00:00:00Z","policy_id":"invalid","policy_version":"v1"}]`
	if os.WriteFile(file, []byte(body), 0600) != nil {
		t.Fatal("fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	os.Args = []string{"observe-jobs", "--dry-run", file}
	if err := run(ctx); err == nil || err.Error() != "invalid dry-run policy binding" {
		t.Fatal("invalid dry-run policy was not rejected before Docker inspection")
	}
	os.Args = []string{"observe-jobs", file}
	if err := run(ctx); err == nil || err.Error() != "workload binding could not be verified" {
		t.Fatal("default correlation CLI began enforcing policy configuration")
	}
	os.Args = []string{"observe-jobs", "--enforce", file}
	if err := run(ctx); err == nil || !strings.HasPrefix(err.Error(), "usage:") {
		t.Fatal("enforcement flag accepted")
	}
}
