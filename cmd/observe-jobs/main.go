// observe-jobs reads private bindings from a trusted controller. It receives no
// OpenBao token, revocation client, or workload-launch configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/grdamico/runawaybao/executionpolicy"
	"github.com/grdamico/runawaybao/policyaudit"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
)

type binding struct {
	PolicyID      string    `json:"policy_id"`
	PolicyVersion string    `json:"policy_version"`
	JobID         string    `json:"job_id"`
	WorkloadID    string    `json:"workload_id"`
	LeaseID       string    `json:"lease_id"`
	StartedAt     time.Time `json:"started_at"`
}

func run(ctx context.Context) error {
	flags := flag.NewFlagSet("observe-jobs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "emit audit-only execution policy decisions")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 1 {
		return errors.New("usage: observe-jobs [--dry-run] PRIVATE_BINDINGS_JSON")
	}
	f, err := os.Open(flags.Arg(0))
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
	var policies *policyaudit.Adapter
	if *dryRun {
		policies, err = policyaudit.New(executionpolicy.DemoPolicies())
		if err != nil {
			return errors.New("invalid dry-run policies")
		}
		for _, b := range bindings {
			if policies.Bind(policyaudit.Binding{JobID: b.JobID, WorkloadID: b.WorkloadID, PolicyID: b.PolicyID, PolicyVersion: b.PolicyVersion}) != nil {
				return errors.New("invalid dry-run policy binding")
			}
		}
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
	audit := policyaudit.NewWriter(os.Stdout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	previousCoverage := runtimeevents.Coverage{}
	for {
		var notice runtimeevents.Notice
		select {
		case <-ticker.C:
			if *dryRun {
				snapshot := consumer.Coverage()
				if snapshot != previousCoverage {
					if audit.Coverage(snapshot) != nil {
						return policyaudit.ErrAudit
					}
					previousCoverage = snapshot
				}
			}
			continue
		case n, ok := <-consumer.Notices:
			if !ok {
				if *dryRun {
					if audit.Coverage(consumer.Coverage()) != nil {
						return policyaudit.ErrAudit
					}
				}
				return nil
			}
			notice = n
		}
		if *dryRun && notice.Gap != nil {
			if audit.Gap(*notice.Gap) != nil {
				return policyaudit.ErrAudit
			}
			if audit.Coverage(consumer.Coverage()) != nil {
				return policyaudit.ErrAudit
			}
			continue
		}

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
		if *dryRun {
			decision := policies.Evaluate(*notice.Event, result, consumer.EffectiveCoverage(notice))
			if audit.Decision(decision) != nil {
				return policyaudit.ErrAudit
			}
			continue
		}
		if encoder.Encode(map[string]any{"status": result.Status, "reason": result.Reason, "job_id": result.JobID, "late": result.Late, "generation": notice.Generation}) != nil {
			return errors.New("output failed")
		}
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
