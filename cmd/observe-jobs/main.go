// observe-jobs reads private bindings from a trusted controller. It receives no
// OpenBao token, revocation client, or workload-launch configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
)

type binding struct {
	JobID      string    `json:"job_id"`
	WorkloadID string    `json:"workload_id"`
	LeaseID    string    `json:"lease_id"`
	StartedAt  time.Time `json:"started_at"`
}

func run(ctx context.Context) error {
	if len(os.Args) != 2 {
		return errors.New("usage: observe-jobs PRIVATE_BINDINGS_JSON")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return errors.New("cannot open private binding file")
	}
	defer f.Close()
	bytes, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(bytes) > 1024*1024 {
		return errors.New("invalid binding file")
	}
	var bindings []binding
	if json.Unmarshal(bytes, &bindings) != nil || len(bindings) == 0 || len(bindings) > 64 {
		return errors.New("invalid binding file")
	}
	metadata := make(map[string]runtimeevents.Metadata)
	for _, b := range bindings {
		if _, exists := metadata[b.JobID]; exists {
			return errors.New("duplicate job binding")
		}
		metadata[b.JobID] = runtimeevents.Metadata{JobID: b.JobID, WorkloadID: b.WorkloadID, LeaseID: b.LeaseID}
	}
	corr := runtimeevents.NewCorrelator(func(id string) (runtimeevents.Metadata, bool) { m, ok := metadata[id]; return m, ok })
	for _, b := range bindings {
		info, err := workload.Inspect(ctx, b.WorkloadID)
		if err != nil || b.StartedAt.IsZero() || !info.StartedAt.Equal(b.StartedAt) || corr.Register(b.WorkloadID, b.JobID) != nil || corr.Activate(info) != nil {
			return errors.New("workload binding could not be verified")
		}
	}
	id, err := workload.Service(ctx, "tetragon")
	if err != nil {
		return errors.New("Tetragon service unavailable")
	}
	consumer := runtimeevents.NewConsumer(runtimeevents.DockerSource(id))
	streamCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = consumer.Run(streamCtx) }()
	defer func() { cancel(); <-done }()
	// Job IDs must be non-sensitive identifiers. Lease IDs and payloads are omitted.
	encoder := json.NewEncoder(os.Stdout)
	for notice := range consumer.Notices {
		if notice.Gap != nil {
			if encoder.Encode(map[string]any{"status": "observation_gap", "reason": notice.Gap.Reason, "generation": notice.Generation}) != nil {
				return errors.New("output failed")
			}
			continue
		}
		if notice.Event == nil {
			continue
		}
		var inventory []workload.Container
		var err error
		if corr.NeedsInventory(*notice.Event) {
			lookupCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			inventory, err = workload.Inventory(lookupCtx)
			stop()
		}
		result := corr.Resolve(*notice.Event, inventory, err == nil)
		if encoder.Encode(map[string]any{"status": result.Status, "reason": result.Reason, "job_id": result.JobID, "late": result.Late, "generation": notice.Generation}) != nil {
			return errors.New("output failed")
		}
	}
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
