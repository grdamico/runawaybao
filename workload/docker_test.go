package workload

import (
	"context"
	"testing"
)

func TestEnvironmentAndOwnership(t *testing.T) {
	t.Setenv("BAO_TOKEN", "sensitive-token")
	t.Setenv("PGPASSWORD", "sensitive-password")
	for _, v := range Environment() {
		if v == "BAO_TOKEN=sensitive-token" || v == "PGPASSWORD=sensitive-password" {
			t.Fatal("credential inherited")
		}
	}
	d, _ := NewDocker()
	if d.Remove(context.Background(), "not-owned") == nil {
		t.Fatal("unowned cleanup accepted")
	}
	if d.Exec(context.Background(), "not-owned") == nil {
		t.Fatal("unowned exec accepted")
	}
}
