//go:build integration

package databasejob_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/grdamico/runawaybao/containerjob"
	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/executionpolicy"
	"github.com/grdamico/runawaybao/jobrunner"
	"github.com/grdamico/runawaybao/policyaudit"
	"github.com/grdamico/runawaybao/runtimeevents"
	"github.com/grdamico/runawaybao/workload"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawScan scans the actual transport before decoding, retaining only a bounded
// suffix. No raw payloads escape the scanner, including when a match is found.
type rawScan struct {
	mu      sync.Mutex
	capture *capturingIssuer
	tail    []byte
	leaked  bool
	count   int64
}

func (s *rawScan) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count += int64(len(p))
	data := append(s.tail, p...)
	s.capture.mu.Lock()
	for _, v := range s.capture.redactionValues {
		if v != "" {
			q, _ := json.Marshal(v)
			if bytes.Contains(data, []byte(v)) || bytes.Contains(data, q[1:len(q)-1]) {
				s.leaked = true
			}
		}
	}
	for _, c := range s.capture.credentials {
		for _, v := range []string{c.Username, c.Password} {
			q, _ := json.Marshal(v)
			if bytes.Contains(data, []byte(v)) || bytes.Contains(data, q[1:len(q)-1]) {
				s.leaked = true
			}
		}
	}
	s.capture.mu.Unlock()
	if len(data) > 16384 {
		data = data[len(data)-16384:]
	}
	s.tail = append([]byte(nil), data...)
	return len(p), nil
}

type scannedStream struct {
	io.Reader
	io.Closer
}

func runContainerDatabase(t *testing.T, capture *capturingIssuer, m *databasejob.Manager, port uint16) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	pgID, err := workload.Service(ctx, "postgres")
	if err != nil {
		t.Fatal("PostgreSQL service unavailable")
	}
	image, err := workload.Image(ctx)
	if err != nil {
		t.Fatal("worker image unavailable")
	}
	docker, err := workload.NewDatabase(pgID, image)
	if err != nil {
		t.Fatal("workload setup failed")
	}
	adapter, err := policyaudit.New(containerjob.Policies())
	if err != nil {
		t.Fatal("policy validation failed")
	}
	corr := runtimeevents.NewCorrelator(func(id string) (runtimeevents.Metadata, bool) {
		r, ok := m.Lookup(id)
		return runtimeevents.Metadata{JobID: r.JobID, WorkloadID: r.WorkloadID, LeaseID: r.LeaseID}, ok
	})
	executor, err := containerjob.New(m, docker, corr, adapter)
	if err != nil {
		t.Fatal("executor setup failed")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 25*time.Second)
		defer stop()
		for _, id := range []string{"A", "B"} {
			if executor.Cleanup(cleanup, id) != nil || executor.Cleanup(cleanup, id) != nil {
				t.Error("owned resource cleanup unconfirmed; journal retained")
			}
		}
	}()
	service, err := workload.Service(ctx, "tetragon")
	if err != nil {
		t.Fatal("Tetragon service unavailable")
	}
	scan := &rawScan{capture: capture}
	source := runtimeevents.DockerSource(service)
	consumer := runtimeevents.NewConsumer(func(ctx context.Context) (io.ReadCloser, error) {
		s, e := source(ctx)
		if e != nil {
			return nil, e
		}
		return scannedStream{Reader: io.TeeReader(s, scan), Closer: s}, nil
	})
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	var auditBytes auditBuffer
	audit := policyaudit.NewWriter(&auditBytes)
	var mu sync.Mutex
	allowed := map[string]bool{}
	suspicious := false
	wrong := false
	child := false
	lineage := false
	unknownSeen := false
	diagnostics := map[string]int{}
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = audit.Coverage(consumer.Coverage())
			case n, ok := <-consumer.Notices:
				if !ok {
					return
				}
				if n.Gap != nil {
					_ = audit.Gap(*n.Gap)
				}
				if n.Event == nil {
					continue
				}
				e := *n.Event
				var inv []workload.Container
				var err error
				if corr.NeedsInventory(e) {
					inv, err = workload.Inventory(ctx)
				}
				r := corr.Resolve(e, inv, err == nil)
				d := adapter.Evaluate(e, r, consumer.EffectiveCoverage(n))
				if audit.Decision(d) != nil {
					t.Error("audit write failed")
					return
				}
				mu.Lock()
				category := "other"
				switch e.Process.Binary {
				case "/runawaybao-db-worker":
					category = "worker"
				case "/runawaybao-unexpected":
					category = "helper"
				case "/runawaybao-netprobe":
					category = "probe"
				}
				if category != "other" {
					diagnostics[category+":"+r.Status+":"+r.Reason+":"+string(d.Outcome)+":"+d.Reason]++
				}
				if r.Status == "correlated" {
					record, _ := m.Lookup(r.JobID)
					if record.WorkloadID != r.ContainerID || record.LeaseID != r.LeaseID || !strings.HasPrefix(r.ContainerID, e.Process.Docker) {
						wrong = true
					}
					if e.Process.Binary == "/runawaybao-db-worker" && d.Outcome == executionpolicy.Allowed {
						allowed[r.JobID] = true
					}
					if e.Process.Binary == "/runawaybao-unexpected" {
						if r.JobID != "B" {
							wrong = true
						}
						child = true
						lineage = e.Process.ParentExecID != "" || e.ParentExecID != ""
						suspicious = d.Outcome == executionpolicy.Suspicious && d.Recommendation == "would_revoke_lease"
					}
				} else {
					unknownSeen = true
					if d.Recommendation != "" {
						wrong = true
					}
				}
				mu.Unlock()
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		<-resolved
		scan.mu.Lock()
		leaked, count := scan.leaked, scan.count
		scan.mu.Unlock()
		if leaked || count == 0 {
			t.Error("raw Tetragon credential redaction not confirmed")
		} else {
			t.Log("PASS: raw Tetragon JSON stream checked privately for credential and token values")
		}
		validatePolicyAudit(t, auditBytes.snapshot(), capture)
	}()
	select {
	case <-consumer.Ready:
	case <-ctx.Done():
		t.Fatal("event stream not established")
	}
	t.Log("PASS: structured stream established before admission")
	runner := jobrunner.NewRunnerWithDatabase(jobrunner.NewPolicy(jobrunner.PermissionDBReadDemo), nil, executor)
	var wg sync.WaitGroup
	for _, id := range []string{"A", "B"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := runner.RunContext(ctx, jobrunner.Job{ID: id, Action: jobrunner.ActionDBReadDemo})
			resultJSON, _ := json.Marshal(r)
			capture.mu.Lock()
			for _, c := range capture.credentials {
				if bytes.Contains(resultJSON, []byte(c.Username)) || bytes.Contains(resultJSON, []byte(c.Password)) {
					t.Error("ordinary job result exposes credentials")
				}
			}
			capture.mu.Unlock()
			if r.Status != jobrunner.Succeeded {
				t.Error("container read failed")
			}
		}()
	}
	wg.Wait()
	a, oka := m.Lookup("A")
	b, okb := m.Lookup("B")
	if !oka || !okb || a.Delivery != "delivered" || b.Delivery != "delivered" || a.LeaseID == b.LeaseID || a.WorkloadID == b.WorkloadID {
		t.Fatal("independent bindings not confirmed")
	}
	ca, cb := capture.get(a.LeaseID), capture.get(b.LeaseID)
	if ca.Username == cb.Username || ca.Password == cb.Password {
		t.Fatal("credentials not unique")
	}
	config, e := pgx.ParseConfig("postgres://unused:unused@127.0.0.1/runawaybao?sslmode=disable&connect_timeout=3")
	if e != nil {
		t.Fatal("inspection configuration failed")
	}
	config.Port = port
	config.User = "runawaybao_admin"
	config.Password = os.Getenv("RUNAWAYBAO_DATABASE_ADMIN_PASSWORD")
	config.Fallbacks = nil
	admin, e := pgx.ConnectConfig(ctx, config)
	if e != nil {
		t.Fatal("private server inspection unavailable")
	}
	defer admin.Close(context.Background())
	verifyOrigin := func() {
		t.Helper()
		for _, id := range []string{"A", "B"} {
			r, _ := docker.Lookup(id)
			ip, e := workload.Address(ctx, r.ContainerID, r.NetworkID)
			if e != nil {
				t.Fatal("container address unavailable")
			}
			record, _ := m.Lookup(id)
			cred := capture.get(record.LeaseID)
			var count int
			e = admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND usename=$2 AND host(client_addr)=$3 AND application_name='runawaybao-db-worker'`, int64(executor.PID(id)), cred.Username, ip).Scan(&count)
			if e != nil || count != 1 {
				t.Fatal("query origin not confirmed by PostgreSQL")
			}
		}
	}
	verifyOrigin()
	t.Log("PASS: PostgreSQL confirms distinct monitored container addresses, credentials and backend sessions for A/B")
	// Credentials are read-only independently of the worker's fixed operation set.
	for _, c := range []struct{ user, password string }{{ca.Username, ca.Password}, {cb.Username, cb.Password}} {
		cfg := config.Copy()
		cfg.User = c.user
		cfg.Password = c.password
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("permission check connection failed")
		}
		_, e = conn.Exec(ctx, "UPDATE demo.fixture SET value=value WHERE id=1")
		conn.Close(ctx)
		var denied *pgconn.PgError
		if !errors.As(e, &denied) || denied.Code != "42501" {
			t.Fatal("unauthorized write denial not confirmed")
		}
	}
	networkChecks(t, ctx, docker, pgID)
	if executor.Child(ctx, "B") != nil {
		t.Fatal("fixed harmless child failed")
	}
	deadline := time.Now().Add(70 * time.Second)
	for {
		mu.Lock()
		complete := allowed["A"] && allowed["B"] && suspicious && child && unknownSeen
		bad := wrong
		mu.Unlock()
		if bad {
			t.Fatal("incorrect event attribution or unsafe unknown recommendation")
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Logf("Safe event diagnostics: %v; A allowed=%t B allowed=%t child=%t suspicious=%t", diagnostics, allowed["A"], allowed["B"], child, suspicious)
			mu.Unlock()
			t.Fatal("worker allowlist and child suspicious decisions not observed")
		}
		select {
		case <-ctx.Done():
			t.Fatal("observation deadline")
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Log("PASS: exact worker paths allowed for A/B; B child correlated only to B and suspicious in dry-run")
	mu.Lock()
	parentMetadata := lineage
	mu.Unlock()
	if !parentMetadata {
		t.Log("LIMITATION: immediate parent metadata absent; child attribution uses its own verified container identity")
	}
	previous := consumer.Stats()
	consumer.Interrupt()
	reconnectDeadline := time.Now().Add(12 * time.Second)
	for consumer.Stats().Connections <= previous.Connections || consumer.Coverage().Status != "observing" {
		if time.Now().After(reconnectDeadline) {
			t.Fatal("observation reconnection not confirmed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if consumer.Coverage().Generation <= previous.Gaps {
		t.Fatal("observation gap lost")
	}
	t.Log("PASS: unknown events untargeted; stream interruption/reconnection retains visible gap generation")
	failureChecks(t, ctx, docker, pgID, image, m, corr, adapter)
	for _, id := range []string{"A", "B"} {
		if executor.Read(ctx, id) != nil {
			t.Fatal("post-policy container read failed")
		}
	}
	verifyOrigin()
	t.Log("PASS: both workers reauthenticated and read after evaluation; no enforcement")
	for _, id := range []string{"A", "B"} {
		r, _ := docker.Lookup(id)
		if workload.CheckRedaction(ctx, r.ContainerID, []string{ca.Username, ca.Password, cb.Username, cb.Password, os.Getenv("RUNAWAYBAO_DATABASE_ROOT_TOKEN"), os.Getenv("RUNAWAYBAO_DATABASE_ADMIN_PASSWORD"), os.Getenv("RUNAWAYBAO_DATABASE_MANAGER_PASSWORD")}) != nil {
			t.Fatal("Docker inspection/log redaction not confirmed")
		}
	}
	t.Log("PASS: private inspection of full Docker configuration and both log streams contains no credential values")
	t.Log("PASS: ordinary job results and sanitized audit records exclude credential values")
	// Explicit lifecycle cleanup, never a policy action. Verify removal and retry.
	for _, id := range []string{"A", "B"} {
		if executor.Finish(ctx, id) != nil {
			t.Fatal("normal worker completion failed")
		}
		if executor.Cleanup(ctx, id) != nil || executor.Cleanup(ctx, id) != nil {
			t.Fatal("owned cleanup failed")
		}
		r, _ := docker.Lookup(id)
		if r.Phase != "cleaned" {
			t.Fatal("cleanup journal lost")
		}
	}
	t.Log("PASS: idempotent owned container/network cleanup; lease metadata retained for natural expiry")
}
func networkChecks(t *testing.T, ctx context.Context, d *workload.Database, pgID string) {
	t.Helper()
	a, _ := d.Lookup("A")
	b, _ := d.Lookup("B")
	ai, e := workload.Address(ctx, a.ContainerID, a.NetworkID)
	if e != nil {
		t.Fatal("A address unavailable")
	}
	bi, e := workload.Address(ctx, b.ContainerID, b.NetworkID)
	if e != nil {
		t.Fatal("B address unavailable")
	}
	for _, id := range []string{"A", "B"} {
		r, _ := d.Lookup(id)
		if !workload.RestrictedDatabase(ctx, r.ContainerID, r.NetworkID) {
			t.Fatal("worker security settings not verified")
		}
		n, e := workload.InspectNetwork(ctx, r.NetworkID)
		if e != nil || !n.Internal || n.EnableIPv6 || n.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || len(n.Containers) != 2 {
			t.Fatal("isolated per-job network not verified")
		}
		if d.ListenProbe(ctx, id) != nil {
			t.Fatal("peer positive control failed")
		}
	}
	time.Sleep(200 * time.Millisecond)
	for _, check := range []struct {
		job, host, port string
		want            bool
	}{{"A", "postgres", "5432", true}, {"B", "postgres", "5432", true}, {"A", "127.0.0.1", "9099", true}, {"B", "127.0.0.1", "9099", true}, {"A", bi, "9099", false}, {"B", ai, "9099", false}} {
		got, e := d.Probe(ctx, check.job, check.host, check.port)
		if e != nil || got != check.want {
			t.Fatal("worker peer/PostgreSQL network boundary failed")
		}
	}
	t.Log("PASS: isolated IPv4-only bridges; PostgreSQL reachable; bidirectional worker-to-worker probes blocked with listening controls")
	bao, e := workload.Service(ctx, "openbao")
	if e != nil {
		t.Fatal("OpenBao unavailable")
	}
	// Read only nonsecret addresses; use PostgreSQL's default network namespace
	// as positive control via bash's TCP primitive (no credentials or output).
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, bao)
	cmd.Env = workload.Environment()
	out, e := cmd.Output()
	if e != nil {
		t.Fatal("OpenBao address unavailable")
	}
	baoIP := strings.TrimSpace(string(out))
	for _, target := range []struct{ host, port, label string }{{baoIP, "8200", "OpenBao"}, {"1.1.1.1", "443", "external IPv4"}, {"host.docker.internal", "18200", "Docker Desktop host published service"}} {
		control := exec.CommandContext(ctx, "docker", "exec", pgID, "timeout", "3", "bash", "-c", `exec 3<>/dev/tcp/"$1"/"$2"`, "probe", target.host, target.port)
		control.Env = workload.Environment()
		control.Stdout = io.Discard
		control.Stderr = io.Discard
		positive := control.Run() == nil
		for _, id := range []string{"A", "B"} {
			reachable, e := d.Probe(ctx, id, target.host, target.port)
			if e != nil {
				t.Fatal("network probe failed")
			}
			if reachable {
				t.Fatalf("network boundary failed: %s reachable", target.label)
			}
		}
		if positive {
			t.Logf("PASS: %s blocked for both workers with reachable control", target.label)
		} else {
			t.Logf("UNCONFIRMED: %s denied probes observed, but positive control unavailable", target.label)
		}
	}
}

func failureChecks(t *testing.T, ctx context.Context, d *workload.Database, pg, image string, m *databasejob.Manager, corr *runtimeevents.Correlator, a *policyaudit.Adapter) {
	t.Helper()
	broken, e := workload.NewDatabase(pg, "sha256:"+strings.Repeat("f", 64))
	if e != nil {
		t.Fatal("failure harness setup")
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if broken.Cleanup(clean, "C") != nil {
			t.Error("partial resource cleanup failed")
		}
	}()
	if m.Prepare(ctx, "C") != nil {
		t.Fatal("failure issuance setup")
	}
	r, e := broken.Create(ctx, "C")
	if e == nil || r.NetworkID == "" || r.ContainerID != "" {
		t.Fatal("container creation failure not retained")
	}
	record, _ := m.Lookup("C")
	if record.LeaseID == "" || record.State != databasejob.Active {
		t.Fatal("issued lease lost on creation failure")
	}
	if broken.Cleanup(ctx, "C") != nil || broken.Cleanup(ctx, "C") != nil {
		t.Fatal("partial network cleanup retry failed")
	}
	// Simulate loss of the delivery acknowledgement after a real attached write.
	// The sink returns a static error, preserving an uncertain delivery outcome.
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if d.Cleanup(clean, "D") != nil {
			t.Error("cancelled worker cleanup failed")
		}
	}()
	if m.Prepare(ctx, "D") != nil {
		t.Fatal("delivery failure setup")
	}
	r, e = d.Create(ctx, "D")
	if e != nil {
		t.Fatal("failure workload create")
	}
	if m.BindWorkload("D", r.ContainerID) != nil || corr.Register(r.ContainerID, "D") != nil || a.Bind(policyaudit.Binding{JobID: "D", WorkloadID: r.ContainerID, PolicyID: "container-reader", PolicyVersion: "v1"}) != nil {
		t.Fatal("failure workload binding")
	}
	active, cancel := context.WithCancel(ctx)
	s, info, e := d.Start(active, "D")
	if e != nil {
		cancel()
		t.Fatal("failure workload start")
	}
	defer cancel()
	defer s.Close()
	if corr.Activate(info) != nil {
		t.Fatal("failure workload activation")
	}
	e = m.Deliver(active, "D", func(ctx context.Context, u, p string) error {
		if s.Deliver(ctx, u, p) != nil {
			t.Error("real delivery failed before simulated acknowledgement loss")
		}
		return errors.New("delivery acknowledgement unavailable")
	})
	if e != databasejob.ErrRead {
		t.Fatal("delivery uncertainty not reported")
	}
	if m.Deliver(active, "D", s.Deliver) != databasejob.ErrInactive {
		t.Fatal("uncertain delivery retried")
	}
	record, _ = m.Lookup("D")
	if record.Delivery != "uncertain" || record.LeaseID == "" || record.WorkloadID != r.ContainerID {
		t.Fatal("uncertain mapping lost")
	}
	cancel()
	_ = s.Close()
	if info, e = workload.Inspect(ctx, r.ContainerID); e == nil && !info.Running {
		_ = corr.Retire(info)
	}
	if d.Cleanup(ctx, "D") != nil || d.Cleanup(ctx, "D") != nil {
		t.Fatal("cancelled resource cleanup retry")
	}
	t.Log("PASS: issued lease retained after failed container creation; uncertain delivery rejects resend; cancellation and owned cleanup remain retryable")
}
