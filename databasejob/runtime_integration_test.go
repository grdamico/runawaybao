//go:build integration

package databasejob_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/jobrunner"
	"github.com/grdamico/runawaybao/openbao"
	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The observation harness never constructs a real revoker or revoker token.
// Unexpected lifecycle cleanup is a failure, rather than an enforcement action.
type forbiddenRevoker struct{}

func (forbiddenRevoker) Revoke(context.Context, string) error {
	return errors.New("revocation unavailable in observation harness")
}

type observed struct {
	event      runtimeevents.Event
	result     runtimeevents.Result
	generation uint64
}

func runObservation(t *testing.T, capture *capturingIssuer, pg *databasejob.PostgreSQL, manager *databasejob.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	service, err := workload.Service(ctx, "tetragon")
	if err != nil {
		t.Fatal("Tetragon service unavailable")
	}
	consumer := runtimeevents.NewConsumer(runtimeevents.DockerSource(service))
	corr := runtimeevents.NewCorrelator(func(id string) (runtimeevents.Metadata, bool) {
		r, ok := manager.Lookup(id)
		return runtimeevents.Metadata{JobID: r.JobID, WorkloadID: r.WorkloadID, LeaseID: r.LeaseID}, ok
	})
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	results := make(chan observed, 512)
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		defer close(results)
		for notice := range consumer.Notices {
			if notice.Event == nil {
				continue
			}
			var inventory []workload.Container
			var err error
			if corr.NeedsInventory(*notice.Event) {
				snapshotCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				inventory, err = workload.Inventory(snapshotCtx)
				stop()
			}
			r := corr.Resolve(*notice.Event, inventory, err == nil)
			select {
			case results <- observed{*notice.Event, r, notice.Generation}:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("event consumer failed graceful shutdown")
		}
		select {
		case <-resolved:
		case <-time.After(6 * time.Second):
			t.Error("resolver did not stop")
		}
	}()
	select {
	case <-consumer.Ready:
	case <-time.After(60 * time.Second):
		t.Fatal("no structured event established observation readiness")
	}
	t.Log("PASS: structured event stream established before any job or workload starts")
	runner := jobrunner.NewRunnerWithDatabase(jobrunner.NewPolicy(jobrunner.PermissionDBReadDemo), nil, manager)
	var wg sync.WaitGroup
	for _, id := range []string{"A", "B"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runner.RunContext(ctx, jobrunner.Job{ID: id, Action: jobrunner.ActionDBReadDemo}).Status != jobrunner.Succeeded {
				t.Error("database job failed")
			}
		}()
	}
	wg.Wait()
	a, _ := manager.Lookup("A")
	b, _ := manager.Lookup("B")
	if a.LeaseID == "" || b.LeaseID == "" || a.LeaseID == b.LeaseID || capture.get(a.LeaseID).Username == capture.get(b.LeaseID).Username {
		t.Fatal("independent leases missing")
	}
	docker, err := workload.NewDocker()
	if err != nil {
		t.Fatal("workload launcher unavailable")
	}
	ids := map[string]string{}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if docker.Close(cleanup) != nil {
			t.Error("owned workload cleanup failed")
		}
	}()
	for _, job := range []string{"A", "B", "unknown"} {
		container, err := docker.Create(ctx)
		if err != nil {
			t.Fatal("restricted workload creation failed")
		}
		ids[job] = container.ID
		if job != "unknown" {
			if manager.BindWorkload(job, container.ID) != nil || corr.Register(container.ID, job) != nil {
				t.Fatal("workload binding failed")
			}
		}
		info, err := docker.Start(ctx, container.ID)
		if err != nil {
			t.Fatal("workload start failed")
		}
		if job != "unknown" && corr.Activate(info) != nil {
			t.Fatal("workload activation failed")
		}
		if !restricted(ctx, container.ID) {
			t.Fatal("workload permissions not restricted")
		}
	}
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if docker.Exec(ctx, id) != nil {
				t.Error("predictable subprocess execution failed")
			}
		}()
	}
	wg.Wait()
	parents := map[string]map[string]bool{"A": {}, "B": {}}
	children := map[string][]string{"A": {}, "B": {}}
	unknown := false
	verified := func() bool {
		return len(parents["A"]) > 0 && len(children["A"]) > 0 && len(parents["B"]) > 0 && len(children["B"]) > 0 && unknown
	}

	reasons := map[string]int{}
	check := func(o observed) {
		if o.result.Reason != "" {
			reasons[o.result.Reason]++
		}
		expected := ""
		for job, id := range ids {
			if o.event.Process.Docker == id[:31] || o.event.Process.Docker == id {
				expected = job
			}
		}
		if expected == "unknown" {
			if o.result.Status == "correlated" {
				t.Fatal("unregistered workload attributed")
			}
			unknown = true
			return
		}
		if expected == "A" || expected == "B" {
			if o.result.Status == "correlated" && o.result.JobID != expected {
				t.Fatal("cross-job attribution")
			}
			if o.result.Status != "correlated" {
				return
			}
			record, _ := manager.Lookup(expected)
			if o.result.LeaseID != record.LeaseID || o.result.ContainerID != ids[expected] {
				t.Fatal("wrong job-to-lease mapping")
			}
			if strings.HasSuffix(o.event.Process.Binary, "/sh") {
				parents[expected][o.event.Process.ExecID] = true
			}
			if strings.HasSuffix(o.event.Process.Binary, "/sleep") {
				children[expected] = append(children[expected], o.event.Process.ParentExecID)
			}
		} else if o.result.Status == "correlated" {
			t.Fatal("unrelated event attributed")
		}
	}
	timer := time.NewTimer(65 * time.Second)
	defer timer.Stop()
	for !verified() {
		select {
		case o, ok := <-results:
			if !ok {
				t.Fatal("event stream stopped")
			}
			check(o)
		case <-timer.C:
			t.Fatalf("correlation incomplete: A parents=%d children=%d; B parents=%d children=%d; unknown=%t; reasons=%v; consumer=%+v", len(parents["A"]), len(children["A"]), len(parents["B"]), len(children["B"]), unknown, reasons, consumer.Stats())
		}
	}
	t.Log("PASS: A/B shell and controlled child executions map independently to their own container, job, and lease; unrelated events remain uncorrelated")
	previous := consumer.Stats().Connections
	consumer.Interrupt()
	resume := time.NewTimer(45 * time.Second)
	defer resume.Stop()
	resumed := false
	for !resumed {
		select {
		case o, ok := <-results:
			if !ok {
				t.Fatal("consumer did not reconnect")
			}
			check(o)
			resumed = consumer.Stats().Connections > previous && o.generation > 0
		case <-resume.C:
			t.Fatal("no structured events after reconnect")
		}
	}
	oldParents := len(parents["A"])
	if docker.Exec(ctx, ids["A"]) != nil {
		t.Fatal("post-reconnect exec failed")
	}
	post := time.NewTimer(45 * time.Second)
	defer post.Stop()
	for len(parents["A"]) == oldParents {
		select {
		case o, ok := <-results:
			if !ok {
				t.Fatal("consumer stopped")
			}
			check(o)
		case <-post.C:
			t.Fatal("no A correlation after reconnect")
		}
	}
	stats := consumer.Stats()
	if stats.Gaps == 0 || stats.Dropped != 0 || stats.Malformed != 0 {
		t.Fatal("unexpected event loss or missing interruption record")
	}
	if manager.Read(ctx, "A") != nil || manager.Read(ctx, "B") != nil {
		t.Fatal("database access was lost during observation")
	}
	afterA, _ := manager.Lookup("A")
	afterB, _ := manager.Lookup("B")
	if afterA.LeaseID != a.LeaseID || afterB.LeaseID != b.LeaseID || afterA.State != databasejob.Active || afterB.State != databasejob.Active {
		t.Fatal("observation changed lease state")
	}
	t.Log("PASS: interruption recorded, stream reconnected, A correlates again, and both original database credentials still read")
	info, err := docker.Stop(ctx, ids["A"])
	if err != nil || corr.Retire(info) != nil {
		t.Fatal("termination metadata failed")
	}
	if docker.Remove(ctx, ids["A"]) != nil {
		t.Fatal("owned removal failed")
	}
	if docker.Close(ctx) != nil {
		t.Fatal("remaining owned cleanup failed")
	}
	inventory, err := workload.Inventory(ctx)
	if err != nil {
		t.Fatal("cleanup inventory failed")
	}
	for _, c := range inventory {
		for _, id := range ids {
			if c.ID == id {
				t.Fatal("owned container remained")
			}
		}
	}
	t.Log("PASS: all three owned workload containers removed; no lease revocation requested")
}
func restricted(ctx context.Context, id string) bool { return workload.Restricted(ctx, id) }
func waitObservationExpiry(t *testing.T, capture *capturingIssuer, port uint16) {
	t.Helper()
	capture.mu.Lock()
	deadline := capture.observationDeadline
	credentials := make([]openbao.DatabaseCredentials, 0, len(capture.credentials))
	for id, c := range capture.credentials {
		credentials = append(credentials, c)
		if capture.deadlines[id].After(deadline) {
			deadline = capture.deadlines[id]
		}
	}
	capture.mu.Unlock()

	t.Log("Waiting for observation leases to expire naturally before bootstrap teardown; no revocation API is used")
	if wait := time.Until(deadline.Add(2 * time.Second)); wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, c := range credentials {
		config, err := pgx.ParseConfig("postgres://unused:unused@127.0.0.1:5432/runawaybao?sslmode=disable&connect_timeout=3")
		if err != nil {
			t.Error("expiry configuration failed")
			continue
		}
		config.Port = port
		config.User = c.Username
		config.Password = c.Password
		config.Fallbacks = nil
		config.Tracer = nil
		conn, err := pgx.ConnectConfig(ctx, config)
		if conn != nil {
			_ = conn.Close(ctx)
		}
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || (pgError.Code != "28P01" && pgError.Code != "28000") {
			t.Error("natural credential expiry not confirmed")
		}
	}
	t.Log("PASS: observation credentials expired naturally before temporary mount/policy teardown")
}
