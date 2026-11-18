package runtimeevents

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseMinimizesPayload(t *testing.T) {
	e, kind, err := Parse([]byte(`{"process_exec":{"process":{"exec_id":"x","pid":12,"docker":"abc","arguments":"sensitive-secret"}},"time":"2026-01-01T00:00:00Z"}`))
	if err != nil || kind != "exec" || e.Process.ExecID != "x" {
		t.Fatal("parse")
	}
	if _, _, err := Parse([]byte(`{"process_exec":{},"process_exit":{}}`)); err == nil {
		t.Fatal("conflicting event types accepted")
	}
}
func TestStreamInterruptionReconnectAndCancellation(t *testing.T) {
	var attempts atomic.Int32
	var writer *io.PipeWriter
	// Each fresh source has a valid event; first stream ends to exercise reconnect.
	c := NewConsumer(func(context.Context) (io.ReadCloser, error) {
		if attempts.Add(1) == 1 {
			return io.NopCloser(strings.NewReader("bad-json\n{\"process_exit\":{}}\n")), nil
		}
		r, w := io.Pipe()
		writer = w
		go func() { _, _ = io.WriteString(w, "{\"process_exec\":{\"process\":{\"exec_id\":\"child\"}}}\n") }()
		return r, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	found := false
	for !found {
		select {
		case n := <-c.Notices:
			found = n.Event != nil
		case <-deadline:
			t.Fatal("no reconnection")
		}
	}
	if stats := c.Stats(); stats.Connections < 2 || stats.Malformed != 1 || stats.Gaps < 2 {
		t.Fatal("missing gap accounting")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked read not canceled")
	}
	_ = writer.Close()
}
func TestOversizeAndQueueBounds(t *testing.T) {
	payload := strings.Repeat("x", MaxFrame+10) + "\n"
	for i := 0; i < 400; i++ {
		payload += "{\"process_exec\":{}}\n"
	}
	c := NewConsumer(func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(payload)), nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for c.Stats().Dropped == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	stats := c.Stats()
	if stats.Malformed == 0 || stats.Dropped == 0 || len(stats.Recent) > 64 || len(c.Notices) > 256 {
		t.Fatal("unbounded or silent loss")
	}
}

func TestRejectsOversizedExtractedMetadata(t *testing.T) {
	payload := `{"process_exec":{"process":{"exec_id":"` + strings.Repeat("x", 1025) + `"}}}`
	if _, _, err := Parse([]byte(payload)); err == nil {
		t.Fatal("oversized extracted metadata accepted")
	}
}

func TestDroppedGapNoticeStillVisible(t *testing.T) {
	c := NewConsumer(nil)
	c.mu.Lock()
	c.observing = true
	c.mu.Unlock()
	old := Notice{Coverage: Coverage{Status: "observing"}}
	for i := 0; i < cap(c.Notices); i++ {
		c.Notices <- Notice{}
	}
	c.gap("queue_overflow")
	if c.Coverage().Generation != 1 || len(c.Notices) != cap(c.Notices) {
		t.Fatal("gap state lost with notification")
	}
	effective := c.EffectiveCoverage(old)
	if effective.Generation != 1 || effective.Status != "unknown" {
		t.Fatal("queued event crossing gap claimed coverage")
	}
	<-c.Notices
	c.emit(Event{})
	for len(c.Notices) > 1 {
		<-c.Notices
	}
	n := <-c.Notices
	if n.Coverage.Generation != 1 || n.Coverage.Status != "observing" {
		t.Fatal("resumed event lost history")
	}
}
