package workload

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/internal/workerprotocol"
)

// Resource is a retained, secret-free cleanup journal. Names are reserved before
// Docker mutations, so lost create responses can be reconciled by name AND owner.
type Resource struct{ JobID, ContainerName, ContainerID, NetworkName, NetworkID, PostgresID, Phase string }
type Database struct {
	mu              sync.Mutex
	docker          *Docker
	postgres, image string
	resources       map[string]Resource
	busy            map[string]bool
}

func NewDatabase(postgresID, imageID string) (*Database, error) {
	if !IDPattern.MatchString(postgresID) || !strings.HasPrefix(imageID, "sha256:") || !IDPattern.MatchString(strings.TrimPrefix(imageID, "sha256:")) {
		return nil, ErrDocker
	}
	d, e := NewDocker()
	if e != nil {
		return nil, e
	}
	return &Database{docker: d, postgres: postgresID, image: imageID, resources: make(map[string]Resource), busy: make(map[string]bool)}, nil
}
func (d *Database) Lookup(job string) (Resource, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.resources[job]
	return r, ok
}
func (d *Database) save(r Resource) { d.mu.Lock(); d.resources[r.JobID] = r; d.mu.Unlock() }
func (d *Database) Create(ctx context.Context, job string) (Resource, error) {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		return Resource{}, ErrDocker
	}
	name := "rbc-" + d.docker.owner + "-" + hex.EncodeToString(b)
	r := Resource{JobID: job, ContainerName: name, NetworkName: name, PostgresID: d.postgres, Phase: "network_create_pending"}
	d.mu.Lock()
	if _, ok := d.resources[job]; ok || job == "" || len(d.resources) >= 32 {
		d.mu.Unlock()
		return Resource{}, ErrDocker
	}
	d.resources[job] = r
	d.busy[job] = true
	d.mu.Unlock()
	defer d.release(job)
	out, e := command(ctx, "network", "create", "--driver", "bridge", "--internal", "--ipv6=false", "--opt", "com.docker.network.bridge.gateway_mode_ipv4=isolated", "--label", ownerLabel+"="+d.docker.owner, r.NetworkName)
	if e != nil {
		return r, ErrDocker
	}
	r.NetworkID = strings.TrimSpace(string(out))
	if !IDPattern.MatchString(r.NetworkID) {
		return r, ErrDocker
	}
	r.Phase = "postgres_attach_pending"
	d.save(r)
	if _, e = command(ctx, "network", "connect", "--alias", "postgres", r.NetworkID, d.postgres); e != nil {
		return r, e
	}
	r.Phase = "container_create_pending"
	d.save(r)
	out, e = command(ctx, "create", "--name", r.ContainerName, "-i", "--label", ownerLabel+"="+d.docker.owner, "--user", "65534:65534", "--network", r.NetworkID, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--pids-limit", "32", "--memory", "64m", "--cpus", "0.25", "--restart", "no", d.image)
	if e != nil {
		return r, e
	}
	r.ContainerID = strings.TrimSpace(string(out))
	if !IDPattern.MatchString(r.ContainerID) {
		return r, ErrDocker
	}
	r.Phase = "created"
	d.save(r)
	d.docker.mu.Lock()
	d.docker.owned[r.ContainerID] = true
	d.docker.mu.Unlock()
	return r, nil
}

// Session is one non-TTY attached stdin stream. No retry/resend occurs after a
// protocol failure. Context cancellation closes the stream; Docker outcome can
// still be ambiguous, so the Resource journal must be reconciled separately.
type Session struct {
	input  *io.PipeWriter
	output io.ReadCloser
	reader *bufio.Reader
	cmd    *exec.Cmd
	once   sync.Once
	gate   chan struct{}
	failed bool
	closed chan struct{}
}

func (s *Session) Close() error {
	s.once.Do(func() {
		close(s.closed)
		_ = s.input.Close()
		_ = s.output.Close()
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	})
	return nil
}
func (d *Database) Start(ctx context.Context, job string) (*Session, Container, error) {
	if !d.acquire(job) {
		return nil, Container{}, ErrDocker
	}
	defer d.release(job)
	r, ok := d.Lookup(job)
	if !ok || r.Phase != "created" {
		return nil, Container{}, ErrDocker
	}
	if _, e := d.docker.verify(ctx, r.ContainerID); e != nil {
		return nil, Container{}, e
	}
	r.Phase = "start_pending"
	d.save(r)
	cmd := exec.Command("docker", "start", "-a", "-i", r.ContainerID)
	cmd.Env = Environment()
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	input, w := io.Pipe()
	cmd.Stdin = input
	out, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		w.Close()
		return nil, Container{}, ErrDocker
	}
	if cmd.Start() != nil {
		input.Close()
		w.Close()
		out.Close()
		return nil, Container{}, ErrDocker
	}
	s := &Session{input: w, output: out, reader: bufio.NewReaderSize(out, workerprotocol.MaxFrame), cmd: cmd, gate: make(chan struct{}, 1), closed: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.closed:
		}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		info, e := Inspect(ctx, r.ContainerID)
		if e == nil && info.Running {
			r.Phase = "bootstrap_waiting"
			d.save(r)
			return s, info, nil
		}
		select {
		case <-ctx.Done():
			s.Close()
			return nil, Container{}, ErrDocker
		case <-deadline.C:
			s.Close()
			return nil, Container{}, ErrDocker
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (s *Session) exchange(ctx context.Context, expected string, send func() error) (workerprotocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return workerprotocol.Response{}, ErrDocker
	}
	defer func() { <-s.gate }()
	if s.failed {
		return workerprotocol.Response{}, ErrDocker
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	if send() != nil {
		s.failed = true
		s.Close()
		return workerprotocol.Response{}, ErrDocker
	}
	var response workerprotocol.Response
	if workerprotocol.Read(s.reader, &response) != nil || ctx.Err() != nil || response.Status != expected || (expected == "read_ok" && response.BackendPID == 0) || (expected != "read_ok" && response.BackendPID != 0) {
		s.failed = true
		s.Close()
		return response, ErrDocker
	}
	return response, nil
}
func (s *Session) Deliver(ctx context.Context, user, password string) error {
	c := workerprotocol.Credentials{Username: user, Password: password}
	if !workerprotocol.Valid(c) {
		return ErrDocker
	}
	response, e := s.exchange(ctx, "ready", func() error {
		n, e := s.input.Write([]byte{'G'})
		if e != nil || n != 1 {
			return ErrDocker
		}
		return workerprotocol.Write(s.input, c)
	})
	if e != nil || response.Status != "ready" {
		return ErrDocker
	}
	return nil
}
func (s *Session) Read(ctx context.Context) (uint32, error) {
	r, e := s.exchange(ctx, "read_ok", func() error { return workerprotocol.Write(s.input, workerprotocol.Command{Operation: "read"}) })
	if e != nil || r.Status != "read_ok" || r.BackendPID == 0 {
		return 0, ErrDocker
	}
	return r.BackendPID, nil
}
func (s *Session) Child(ctx context.Context) error {
	r, e := s.exchange(ctx, "child_ok", func() error { return workerprotocol.Write(s.input, workerprotocol.Command{Operation: "demo-child"}) })
	if e != nil || r.Status != "child_ok" {
		return ErrDocker
	}
	return nil
}
func (s *Session) Finish(ctx context.Context) error {
	r, e := s.exchange(ctx, "finished", func() error { return workerprotocol.Write(s.input, workerprotocol.Command{Operation: "finish"}) })
	if e != nil || r.Status != "finished" {
		return ErrDocker
	}
	return s.Close()
}

// Cleanup reconciles every resource by exact name plus random ownership label.
// Failed cleanup retains the journal entry for retry; unrelated members refuse deletion.
func (d *Database) Cleanup(ctx context.Context, job string) error {
	if !d.acquire(job) {
		return ErrDocker
	}
	defer d.release(job)
	r, ok := d.Lookup(job)
	if !ok {
		return ErrDocker
	}
	if r.Phase == "cleaned" {
		return nil
	}
	r.Phase = "cleanup_pending"
	d.save(r)
	out, e := command(ctx, "ps", "-aq", "--no-trunc", "--filter", "name=^/"+r.ContainerName+"$")
	if e != nil {
		return e
	}
	ids := strings.Fields(string(out))
	if len(ids) > 1 {
		return ErrDocker
	}
	if len(ids) == 0 && r.ContainerID != "" {
		all, err := Inventory(ctx)
		if err != nil {
			return ErrDocker
		}
		for _, info := range all {
			if info.ID == r.ContainerID {
				return ErrDocker
			}
		}
	}
	if len(ids) == 1 {
		if r.ContainerID != "" && r.ContainerID != ids[0] {
			return ErrDocker
		}
		r.ContainerID = ids[0]
		d.save(r)
		d.docker.mu.Lock()
		d.docker.owned[r.ContainerID] = true
		d.docker.mu.Unlock()
		if d.docker.Remove(ctx, r.ContainerID) != nil {
			return ErrDocker
		}
	}
	out, e = command(ctx, "network", "ls", "-q", "--no-trunc", "--filter", "name=^"+r.NetworkName+"$")
	if e != nil {
		return e
	}
	ids = strings.Fields(string(out))
	if len(ids) == 0 && r.NetworkID != "" {
		all, err := command(ctx, "network", "ls", "-q", "--no-trunc")
		if err != nil {
			return ErrDocker
		}
		for _, id := range strings.Fields(string(all)) {
			if id == r.NetworkID {
				return ErrDocker
			}
		}
	}
	if len(ids) > 1 {
		return ErrDocker
	}
	if len(ids) == 1 {
		if r.NetworkID != "" && r.NetworkID != ids[0] {
			return ErrDocker
		}
		r.NetworkID = ids[0]
		d.save(r)
		network, e := InspectNetwork(ctx, r.NetworkID)
		if e != nil || network.Owner != d.docker.owner {
			return ErrDocker
		}
		for id := range network.Containers {
			if id != r.PostgresID {
				return ErrDocker
			}
		}
		if _, ok := network.Containers[r.PostgresID]; ok {
			if _, e = command(ctx, "network", "disconnect", r.NetworkID, r.PostgresID); e != nil {
				return e
			}
		}
		if _, e = command(ctx, "network", "rm", r.NetworkID); e != nil {
			return e
		}
	}
	r.Phase = "cleaned"
	d.save(r)
	return nil
}

type Network struct {
	ID         string `json:"Id"`
	Internal   bool
	EnableIPv6 bool
	Options    map[string]string
	Labels     map[string]string
	Containers map[string]struct{ IPv4Address string }
	Owner      string `json:"-"`
}

func InspectNetwork(ctx context.Context, id string) (Network, error) {
	if !IDPattern.MatchString(id) {
		return Network{}, ErrDocker
	}
	b, e := command(ctx, "network", "inspect", "--format", "{{json .}}", id)
	if e != nil {
		return Network{}, e
	}
	var n Network
	if json.Unmarshal(b, &n) != nil || n.ID != id {
		return n, ErrDocker
	}
	n.Owner = n.Labels[ownerLabel]
	return n, nil
}
func Address(ctx context.Context, id, network string) (string, error) {
	n, e := InspectNetwork(ctx, network)
	if e != nil {
		return "", e
	}
	endpoint, ok := n.Containers[id]
	if !ok {
		return "", ErrDocker
	}
	host, _, e := net.ParseCIDR(endpoint.IPv4Address)
	if e != nil {
		return "", ErrDocker
	}
	return host.String(), nil
}
func Image(ctx context.Context) (string, error) {
	out, e := command(ctx, "image", "inspect", "--format", "{{.Id}}", "runawaybao-db-worker:local")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(out)), nil
}
func (d *Database) Probe(ctx context.Context, job, host, port string) (bool, error) {
	r, ok := d.Lookup(job)
	if !ok {
		return false, ErrDocker
	}
	if _, e := d.docker.verify(ctx, r.ContainerID); e != nil {
		return false, e
	}
	if net.ParseIP(host) == nil && host != "host.docker.internal" && host != "postgres" {
		return false, ErrDocker
	}
	if port != "5432" && port != "8200" && port != "18200" && port != "443" && port != "9099" {
		return false, ErrDocker
	}
	out, e := command(ctx, "exec", r.ContainerID, "/runawaybao-netprobe", host, port)
	if e != nil {
		return false, e
	}
	s := strings.TrimSpace(string(out))
	if s != "reachable" && s != "blocked" {
		return false, ErrDocker
	}
	return s == "reachable", nil
}
func (d *Database) ListenProbe(ctx context.Context, job string) error {
	r, ok := d.Lookup(job)
	if !ok {
		return ErrDocker
	}
	if _, e := d.docker.verify(ctx, r.ContainerID); e != nil {
		return e
	}
	_, e := command(ctx, "exec", "-d", r.ContainerID, "/runawaybao-netprobe", "listen")
	return e
}

// CheckRedaction reads sensitive inspection/log output privately, never returning it.
func CheckRedaction(ctx context.Context, id string, secrets []string) error {
	if !IDPattern.MatchString(id) {
		return ErrDocker
	}
	for _, args := range [][]string{{"inspect", id}, {"logs", id}} {
		b, e := privateOutput(ctx, args...)
		if e != nil {
			return e
		}
		for _, secret := range secrets {
			if secret != "" && bytesContain(b, secret) {
				return ErrDocker
			}
		}
	}
	return nil
}
func bytesContain(b []byte, s string) bool {
	if strings.Contains(string(b), s) {
		return true
	}
	encoded, _ := json.Marshal(s)
	return len(encoded) > 2 && strings.Contains(string(b), string(encoded[1:len(encoded)-1]))
}

// privateOutput captures both streams into bounded storage for redaction checks.
func privateOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = Environment()
	cmd.WaitDelay = 2 * time.Second
	var b limitedBuffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if cmd.Run() != nil {
		return nil, ErrDocker
	}
	return b.Bytes(), nil
}

func (d *Database) acquire(job string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.resources[job]; !ok || d.busy[job] {
		return false
	}
	d.busy[job] = true
	return true
}
func (d *Database) release(job string) { d.mu.Lock(); d.busy[job] = false; d.mu.Unlock() }
