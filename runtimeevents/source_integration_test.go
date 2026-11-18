//go:build integration

package runtimeevents

import (
	"context"
	"github.com/grdamico/runawaybao/workload"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveSource(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_TETRA_SOURCE_INTEGRATION") != "1" {
		t.Skip("explicit live source opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	id, err := workload.Service(ctx, "tetragon")
	if err != nil {
		t.Fatal("service unavailable")
	}
	before, err := workload.SourceProcesses(ctx, id)
	if err != nil {
		t.Fatal("source process inspection failed")
	}
	c := NewConsumer(DockerSource(id))
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-c.Ready:
		t.Log("PASS: structured JSON received")
	case <-time.After(40 * time.Second):
		t.Error("no structured event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("source shutdown blocked")
	}
	check, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	after, err := workload.SourceProcesses(check, id)
	if err != nil || after > before {
		t.Fatalf("source reader cleanup not confirmed: before=%d after=%d", before, after)
	}
	t.Log("PASS: source shutdown did not leave an additional tetra reader")
}

func TestLiveWorkloadIdentity(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_TETRA_SOURCE_INTEGRATION") != "1" {
		t.Skip("explicit live source opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	service, err := workload.Service(ctx, "tetragon")
	if err != nil {
		t.Fatal("service")
	}
	c := NewConsumer(DockerSource(service))
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-c.Ready:
	case <-ctx.Done():
		t.Fatal("readiness")
	}
	d, _ := workload.NewDocker()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if d.Close(cleanup) != nil {
			t.Error("cleanup")
		}
	}()
	container, err := d.Create(ctx)
	if err != nil {
		t.Fatal("create")
	}
	info, err := d.Start(ctx, container.ID)
	if err != nil {
		t.Fatal("start")
	}
	if d.Exec(ctx, container.ID) != nil {
		t.Fatal("exec")
	}
	paths := map[string]bool{}
	stats := map[string]int{}
	shellIDs := map[string]bool{}
	childParents := []string{}
	topParents := []string{}
	shellPIDs := map[uint32]bool{}
	topPIDs := []uint32{}
	timer := time.NewTimer(55 * time.Second)
	defer timer.Stop()
	for {
		select {
		case notice := <-c.Notices:
			if notice.Event == nil {
				continue
			}
			e := *notice.Event
			if e.Process.Docker != "" {
				stats["container_event"]++
			}
			if strings.HasSuffix(e.Process.Binary, "/sleep") {
				stats["sleep_event"]++
			}
			if e.Process.Docker == container.ID[:31] || e.Process.Docker == container.ID {
				paths[e.Process.Binary] = true
				if e.Process.Start.Before(info.StartedAt) {
					t.Fatal("execution predates workload instance")
				}
				if strings.HasSuffix(e.Process.Binary, "/sh") {
					shellIDs[e.Process.ExecID] = true
					shellPIDs[e.Process.PID] = true
				}
				if strings.HasSuffix(e.Process.Binary, "/sleep") {
					childParents = append(childParents, e.Process.ParentExecID)
					topParents = append(topParents, e.ParentExecID)
					topPIDs = append(topPIDs, e.ParentPID)
				}
				if len(shellIDs) > 0 && len(childParents) >= 1 {
					direct, top, pid := false, false, false
					for _, id := range childParents {
						direct = direct || shellIDs[id]
					}
					for _, id := range topParents {
						top = top || shellIDs[id]
					}
					for _, id := range topPIDs {
						pid = pid || shellPIDs[id]
					}
					t.Logf("Controlled executable paths: %v", paths)
					t.Logf("PASS: controlled shell and child have verified container identity; optional parent linkage: process.parent_exec_id=%t event.parent.exec_id=%t event.parent.pid=%t", direct, top, pid)
					return
				}
			}
		case <-timer.C:
			t.Fatalf("no own workload identity: counts=%v consumer=%+v", stats, c.Stats())
		}
	}
}
