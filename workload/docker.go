// Package workload launches restricted, secret-free observation containers.
// Docker access belongs to the trusted controller, never to a workload.
package workload

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrDocker = errors.New("Docker operation failed; diagnostics suppressed")
var IDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const ownerLabel = "runawaybao.observation-owner"
const inspectFormat = `{"id":{{json .Id}},"started_at":{{json .State.StartedAt}},"finished_at":{{json .State.FinishedAt}},"running":{{json .State.Running}},"restarts":{{json .RestartCount}},"owner":{{json (index .Config.Labels "runawaybao.observation-owner")}}}`

type Container struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Running    bool      `json:"running"`
	Restarts   int       `json:"restarts"`
	Owner      string    `json:"owner"`
}
type Docker struct {
	owner    string
	mu       sync.Mutex
	owned    map[string]bool
	creating int
}

func NewDocker() (*Docker, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return nil, ErrDocker
	}
	return &Docker{owner: hex.EncodeToString(b), owned: make(map[string]bool)}, nil
}

// Environment deliberately excludes BAO/PG/bootstrap tokens and passwords.
func Environment() []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR", "SystemRoot"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, ErrDocker
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = Environment()
	cmd.Stderr = io.Discard
	var b limitedBuffer
	cmd.Stdout = &b
	cmd.WaitDelay = 2 * time.Second
	if cmd.Run() != nil {
		return nil, ErrDocker
	}
	return b.Bytes(), nil
}
func Inspect(ctx context.Context, id string) (Container, error) {
	if !IDPattern.MatchString(id) {
		return Container{}, ErrDocker
	}
	b, e := command(ctx, "inspect", "--format", inspectFormat, id)
	if e != nil {
		return Container{}, e
	}
	var c Container
	if json.Unmarshal(b, &c) != nil || c.ID != id {
		return Container{}, ErrDocker
	}
	return c, nil
}

// Inventory is a fresh view of all containers, including unrelated stopped ones.
// Refuse an oversized inventory rather than resolve identities from a partial set.
func Inventory(ctx context.Context) ([]Container, error) {
	b, e := command(ctx, "ps", "-aq", "--no-trunc")
	if e != nil {
		return nil, e
	}
	ids := strings.Fields(string(b))
	if len(ids) > 256 {
		return nil, ErrDocker
	}
	if len(ids) == 0 {
		return nil, nil
	}
	args := []string{"inspect", "--format", inspectFormat}
	for _, id := range ids {
		if !IDPattern.MatchString(id) {
			return nil, ErrDocker
		}
		args = append(args, id)
	}
	b, e = command(ctx, args...)
	if e != nil {
		return nil, e
	}
	var result []Container
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var c Container
		if dec.Decode(&c) != nil || !IDPattern.MatchString(c.ID) {
			return nil, ErrDocker
		}
		result = append(result, c)
	}
	if len(result) != len(ids) {
		return nil, ErrDocker
	}
	return result, nil
}
func Service(ctx context.Context, name string) (string, error) {
	if name != "tetragon" && name != "postgres" && name != "openbao" {
		return "", ErrDocker
	}
	b, e := command(ctx, "ps", "--no-trunc", "-q", "--filter", "label=com.docker.compose.project=runawaybao", "--filter", "label=com.docker.compose.service="+name)
	ids := strings.Fields(string(b))
	if e != nil || len(ids) != 1 || !IDPattern.MatchString(ids[0]) {
		return "", ErrDocker
	}
	return ids[0], nil
}
func (d *Docker) Create(ctx context.Context) (Container, error) {
	d.mu.Lock()
	if len(d.owned)+d.creating >= 32 {
		d.mu.Unlock()
		return Container{}, ErrDocker
	}
	d.creating++
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.creating--; d.mu.Unlock() }()
	// Resolve the version tag to an immutable local image ID for this invocation.
	image, e := command(ctx, "image", "inspect", "--format", "{{.Id}}", "alpine:3.22")
	if e != nil {
		return Container{}, e
	}
	imageID := strings.TrimSpace(string(image))
	if !strings.HasPrefix(imageID, "sha256:") || !IDPattern.MatchString(strings.TrimPrefix(imageID, "sha256:")) {
		return Container{}, ErrDocker
	}
	b, e := command(ctx, "create", "--label", ownerLabel+"="+d.owner, "--user", "65534:65534", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--pids-limit", "32", "--memory", "64m", "--cpus", "0.25", "--restart", "no", imageID, "tail", "-f", "/dev/null")
	if e != nil {
		return Container{}, e
	}
	id := strings.TrimSpace(string(b))
	if !IDPattern.MatchString(id) {
		return Container{}, ErrDocker
	}
	d.mu.Lock()
	d.owned[id] = true
	d.mu.Unlock()
	return Container{ID: id, Owner: d.owner}, nil
}
func (d *Docker) verify(ctx context.Context, id string) (Container, error) {
	d.mu.Lock()
	ok := d.owned[id]
	d.mu.Unlock()
	if !ok {
		return Container{}, ErrDocker
	}
	c, e := Inspect(ctx, id)
	if e != nil || c.Owner != d.owner {
		return Container{}, ErrDocker
	}
	return c, nil
}
func (d *Docker) Start(ctx context.Context, id string) (Container, error) {
	if _, e := d.verify(ctx, id); e != nil {
		return Container{}, e
	}
	if _, e := command(ctx, "start", id); e != nil {
		return Container{}, e
	}
	return Inspect(ctx, id)
}

// Exec runs only this fixed harmless parent/child demonstration, never caller SQL
// or a command string supplied by a job.
func (d *Docker) Exec(ctx context.Context, id string) error {
	if _, e := d.verify(ctx, id); e != nil {
		return e
	}
	_, e := command(ctx, "exec", "--user", "65534:65534", id, "/bin/sh", "-c", "/bin/sleep 2 & wait")
	return e
}
func (d *Docker) Remove(ctx context.Context, id string) error {
	if _, e := d.verify(ctx, id); e != nil {
		return e
	}
	if _, e := command(ctx, "rm", "-f", id); e != nil {
		return e
	}
	d.mu.Lock()
	delete(d.owned, id)
	d.mu.Unlock()
	return nil
}
func (d *Docker) Close(ctx context.Context) error {
	d.mu.Lock()
	ids := make([]string, 0, len(d.owned))
	for id := range d.owned {
		ids = append(ids, id)
	}
	d.mu.Unlock()
	var failed bool
	for _, id := range ids {
		if d.Remove(ctx, id) != nil {
			failed = true
		}
	}
	if failed {
		return ErrDocker
	}
	return nil
}

func (d *Docker) Stop(ctx context.Context, id string) (Container, error) {
	if _, e := d.verify(ctx, id); e != nil {
		return Container{}, e
	}
	if _, e := command(ctx, "stop", "--time", "2", id); e != nil {
		return Container{}, e
	}
	return Inspect(ctx, id)
}

// Restricted checks the isolation settings without returning environment values.
func Restricted(ctx context.Context, id string) bool { return restrictedOn(ctx, id, "none") }

func RestrictedDatabase(ctx context.Context, id, networkID string) bool {
	if !IDPattern.MatchString(networkID) {
		return false
	}
	return restrictedOn(ctx, id, networkID)
}

func restrictedOn(ctx context.Context, id, network string) bool {
	if !IDPattern.MatchString(id) {
		return false
	}
	const format = `{"user":{{json .Config.User}},"privileged":{{json .HostConfig.Privileged}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"network":{{json .HostConfig.NetworkMode}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"pidmode":{{json .HostConfig.PidMode}},"binds":{{json .HostConfig.Binds}},"memory":{{json .HostConfig.Memory}},"pids":{{json .HostConfig.PidsLimit}}}`
	b, e := command(ctx, "inspect", "--format", format, id)
	if e != nil {
		return false
	}
	var s struct {
		User                 string
		Privileged, Readonly bool
		Network              string
		Caps, Security       []string
		PIDMode              string
		Binds                []string
		Memory, Pids         int64
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	return s.User == "65534:65534" && !s.Privileged && s.Readonly && s.Network == network && s.PIDMode == "" && len(s.Binds) == 0 && s.Memory > 0 && s.Pids > 0 && len(s.Caps) == 1 && s.Caps[0] == "ALL" && len(s.Security) == 1 && s.Security[0] == "no-new-privileges:true"
}

// SourceProcesses counts CLI readers without exposing host command arguments.
func SourceProcesses(ctx context.Context, id string) (int, error) {
	if !IDPattern.MatchString(id) {
		return 0, ErrDocker
	}
	b, e := command(ctx, "exec", id, "sh", "-c", `ps -eo comm | awk '$1 == "tetra" {n++} END {print n+0}'`)
	if e != nil {
		return 0, e
	}
	n, e := strconv.Atoi(strings.TrimSpace(string(b)))
	if e != nil {
		return 0, ErrDocker
	}
	return n, nil
}
