package workload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture CLI simulates mutations with lost responses and a foreign owner. It
// never contacts Docker; tests exercise journal retention and ownership refusal.
func fakeDocker(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+body), 0700) != nil {
		t.Fatal("fixture")
	}
	t.Setenv("PATH", dir)
}
func TestAmbiguousCreateAndCleanupRetry(t *testing.T) {
	fakeDocker(t, "exit 1\n")
	d, _ := NewDatabase(strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64))
	r, e := d.Create(context.Background(), "A")
	if e == nil || r.NetworkName == "" || r.Phase != "network_create_pending" {
		t.Fatal("lost pre-mutation journal")
	}
	if _, e = d.Create(context.Background(), "A"); e == nil {
		t.Fatal("duplicate retry created resources")
	}
	if d.Cleanup(context.Background(), "A") == nil {
		t.Fatal("failed discovery reported success")
	}
	r, _ = d.Lookup("A")
	if r.Phase != "cleanup_pending" || r.NetworkName == "" {
		t.Fatal("failed cleanup lost metadata")
	}
	fakeDocker(t, "exit 0\n")
	if d.Cleanup(context.Background(), "A") != nil || d.Cleanup(context.Background(), "A") != nil {
		t.Fatal("confirmed absence not idempotent")
	}
	r, _ = d.Lookup("A")
	if r.Phase != "cleaned" || r.NetworkName == "" {
		t.Fatal("cleanup erased journal")
	}
}
func TestCleanupRejectsForeignContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	fakeDocker(t, "case \"$1\" in ps) echo "+id+";; inspect) echo '{\"id\":\""+id+"\",\"owner\":\"foreign\"}' ;; *) exit 9;; esac\n")
	d, _ := NewDatabase(strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64))
	d.resources["A"] = Resource{JobID: "A", ContainerName: "reserved", NetworkName: "reserved", Phase: "created"}
	if d.Cleanup(context.Background(), "A") == nil {
		t.Fatal("foreign cleanup accepted")
	}
	r, _ := d.Lookup("A")
	if r.Phase != "cleanup_pending" || r.ContainerID != id {
		t.Fatal("discovered resource not retained")
	}
}
func TestConcurrentLifecycleRefusesCleanup(t *testing.T) {
	d, _ := NewDatabase(strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64))
	d.resources["A"] = Resource{JobID: "A"}
	d.busy["A"] = true
	if d.Cleanup(context.Background(), "A") == nil {
		t.Fatal("in-flight cleanup accepted")
	}
}
func TestCleanupRejectsForeignNetwork(t *testing.T) {
	id := strings.Repeat("a", 64)
	fakeDocker(t, "case \"$1 $2\" in 'ps -aq') exit 0;; 'network ls') echo "+id+";; 'network inspect') echo '{\"Id\":\""+id+"\",\"Labels\":{\"runawaybao.observation-owner\":\"foreign\"}}';; *) exit 9;; esac\n")
	d, _ := NewDatabase(strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64))
	d.resources["A"] = Resource{JobID: "A", ContainerName: "reserved", NetworkName: "reserved", Phase: "postgres_attach_pending"}
	if d.Cleanup(context.Background(), "A") == nil {
		t.Fatal("foreign network cleanup accepted")
	}
	r, _ := d.Lookup("A")
	if r.Phase != "cleanup_pending" || r.NetworkID != id {
		t.Fatal("network journal lost")
	}
}
