package runtimeevents

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/workload"
)

var ErrSource = errors.New("runtime event source unavailable")

type dockerStream struct {
	io.ReadCloser
	input *io.PipeWriter
	cmd   *exec.Cmd
	once  sync.Once
}

func (s *dockerStream) Close() error {
	s.once.Do(func() {
		_ = s.input.Close()
		_ = s.ReadCloser.Close()
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

// DockerSource streams only from the selected, project-scoped Tetragon service.
// The shell guard ties remote tetra lifetime to stdin EOF; a remote time limit
// additionally bounds orphan lifetime if Docker client/daemon connectivity fails.
// No environment from controller bootstrap is forwarded into the container.
func DockerSource(containerID string) Source {
	return func(ctx context.Context) (io.ReadCloser, error) {
		if !workload.IDPattern.MatchString(containerID) {
			return nil, ErrSource
		}
		const script = `exec 3<&0; tetra getevents -o json </dev/null & child=$!; (read -r ignored <&3; kill "$child" 2>/dev/null) & guard=$!; wait "$child"; result=$?; kill "$guard" 2>/dev/null; wait "$guard" 2>/dev/null; exit "$result"`
		cmd := exec.Command("docker", "exec", "-i", containerID, "timeout", "180", "sh", "-c", script)
		cmd.Env = workload.Environment()
		cmd.Stderr = io.Discard
		cmd.WaitDelay = 2 * time.Second
		input, writer := io.Pipe()
		cmd.Stdin = input
		output, err := cmd.StdoutPipe()
		if err != nil {
			_ = input.Close()
			_ = writer.Close()
			return nil, ErrSource
		}
		if cmd.Start() != nil {
			_ = input.Close()
			_ = writer.Close()
			_ = output.Close()
			return nil, ErrSource
		}
		// exec owns the stdin copier. Closing writer releases it on cancellation.
		return &dockerStream{ReadCloser: output, input: writer, cmd: cmd}, nil
	}
}
