//go:build integration

package workload

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveLifecycle(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_WORKLOAD_INTEGRATION") != "1" {
		t.Skip("explicit workload opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := NewDocker()
	if err != nil {
		t.Fatal("launcher")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if d.Close(cleanup) != nil {
			t.Error("cleanup")
		}
	}()
	c, err := d.Create(ctx)
	if err != nil {
		t.Fatal("create")
	}
	info, err := d.Start(ctx, c.ID)
	if err != nil || !info.Running {
		t.Fatal("start")
	}
	if !Restricted(ctx, c.ID) {
		t.Fatal("permissions")
	}
	if d.Exec(ctx, c.ID) != nil {
		t.Fatal("child command")
	}
	info, err = d.Stop(ctx, c.ID)
	if err != nil || info.Running {
		t.Fatal("stop")
	}
	if d.Remove(ctx, c.ID) != nil {
		t.Fatal("remove")
	}
	inventory, err := Inventory(ctx)
	if err != nil {
		t.Fatal("inventory")
	}
	for _, other := range inventory {
		if other.ID == c.ID {
			t.Fatal("cleanup not complete")
		}
	}
	t.Log("PASS: restricted create/start/child execution/stop/remove verified")
}
