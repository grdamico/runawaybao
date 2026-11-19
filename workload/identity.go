package workload

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Identity pins the namespace objects in the trusted agent's host view until
// the controller releases them. Only numeric namespace identities cross the pipe.
type Identity struct {
	info        Container
	pid, cgroup uint32
	input       *io.PipeWriter
	done        chan struct{}
	closed      chan struct{}
	once        sync.Once
	expires     time.Time
}

func (i *Identity) Close() { i.once.Do(func() { close(i.closed); _ = i.input.Close() }) }

func (i *Identity) Namespaces(info Container) (uint32, uint32, bool) {
	select {
	case <-i.closed:
		return 0, 0, false
	case <-i.done:
		return 0, 0, false
	default:
	}
	if !time.Now().Before(i.expires) || !info.Running || info.Restarts != 0 || info.ID != i.info.ID ||
		info.PID != i.info.PID || !info.StartedAt.Equal(i.info.StartedAt) || info.Owner != i.info.Owner {
		return 0, 0, false
	}
	return i.pid, i.cgroup, true
}

const pinNamespaces = `
set -eu
p=/proc/$1
cg=$(cat "$p/cgroup")
case "$cg" in
  "0::/docker/$2"|"0::/system.slice/docker-$2.scope") ;;
  *) exit 1 ;;
esac
exec 3<"$p/ns/pid" 4<"$p/ns/cgroup"
pid=$(stat -Lc %i /proc/self/fd/3)
cgroup=$(stat -Lc %i /proc/self/fd/4)
test "$pid" != "$(stat -Lc %i /proc/1/ns/pid)"
test "$cgroup" != "$(stat -Lc %i /proc/1/ns/cgroup)"
test "$pid" = "$(stat -Lc %i "$p/ns/pid")"
test "$cgroup" = "$(stat -Lc %i "$p/ns/cgroup")"
test "$cg" = "$(cat "$p/cgroup")"
echo "$pid $cgroup"
read -r ignored || :
`

// PinIdentity checks a controller-owned, running generation using procfs in
// the trusted Tetragon service. It supports only known cgroup-v2 Docker layouts.
func (d *Database) PinIdentity(ctx context.Context, agent, job string, info Container) (*Identity, error) {
	r, ok := d.Lookup(job)
	if !ok || r.ContainerID != info.ID || info.Owner != d.docker.owner ||
		!info.Running || info.Restarts != 0 || info.PID == 0 || info.PIDMode != "" || info.CgroupMode != "private" || !IDPattern.MatchString(agent) {
		return nil, ErrDocker
	}
	check, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if current, err := d.docker.verify(check, info.ID); err != nil || current != info {
		return nil, ErrDocker
	}
	expires := time.Now().Add(175 * time.Second)
	reader, writer := io.Pipe()
	cmd := exec.Command("docker", "exec", "-i", agent, "timeout", "180", "sh", "-c", pinNamespaces,
		"runawaybao-pin", fmt.Sprint(info.PID), info.ID)
	cmd.Env = Environment()
	cmd.Stdin = reader
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, ErrDocker
	}
	proof := &Identity{info: info, input: writer, done: make(chan struct{}), closed: make(chan struct{}), expires: expires}
	line := make(chan string, 1)
	go func() {
		b := bufio.NewReaderSize(output, 64)
		s, err := b.ReadSlice('\n')
		if err != nil {
			line <- ""
		} else {
			line <- string(s)
			_, _ = io.Copy(io.Discard, b)
		}
		close(proof.done)
		_ = reader.Close()
		_ = cmd.Wait()
	}()
	select {
	case <-check.Done():
		proof.Close()
		_ = cmd.Process.Kill()
		return nil, ErrDocker
	case s := <-line:
		fields := strings.Fields(s)
		if len(fields) != 2 {
			proof.Close()
			return nil, ErrDocker
		}
		if n, _ := fmt.Sscan(s, &proof.pid, &proof.cgroup); n != 2 || proof.pid == 0 || proof.cgroup == 0 {
			proof.Close()
			return nil, ErrDocker
		}
	}
	if current, err := d.docker.verify(check, info.ID); err != nil || current != info {
		proof.Close()
		return nil, ErrDocker
	}
	go func() {
		select {
		case <-ctx.Done():
			proof.Close()
		case <-proof.done:
		}
	}()
	return proof, nil
}
