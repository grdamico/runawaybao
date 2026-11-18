package workload

import (
	"bufio"
	"context"
	"github.com/grdamico/runawaybao/internal/workerprotocol"
	"io"
	"os/exec"
	"testing"
	"time"
)

func fixtureSession(t *testing.T) *Session {
	t.Helper()
	cmd := exec.Command("/bin/cat")
	cmd.Env = Environment()
	r, w := io.Pipe()
	cmd.Stdin = r
	out, e := cmd.StdoutPipe()
	if e != nil || cmd.Start() != nil {
		t.Fatal("fixture process")
	}
	s := &Session{input: w, output: out, reader: bufio.NewReaderSize(out, workerprotocol.MaxFrame), cmd: cmd, gate: make(chan struct{}, 1), closed: make(chan struct{})}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestSessionCancellation(t *testing.T) {
	s := fixtureSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := s.exchange(ctx, "ready", func() error { return nil }); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked protocol read ignored cancellation")
	}
	if _, e := s.Read(context.Background()); e == nil {
		t.Fatal("failed stream reused")
	}
}
func TestInvalidResponseTaintsSession(t *testing.T) {
	s := fixtureSession(t)
	if s.Deliver(context.Background(), "sensitive-user", "sensitive-password") == nil {
		t.Fatal("credential echo accepted as response")
	}
	if _, e := s.Read(context.Background()); e == nil {
		t.Fatal("ambiguous stream reused")
	}
}
