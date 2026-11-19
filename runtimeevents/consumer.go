package runtimeevents

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const MaxFrame = 1024 * 1024

type Source func(context.Context) (io.ReadCloser, error)
type Gap struct {
	At         time.Time
	Reason     string
	Generation uint64
}
type Coverage struct {
	Status     string
	Generation uint64
}

type Notice struct {
	origin     *Consumer
	Coverage   Coverage
	Event      *Event
	Gap        *Gap
	Generation uint64
}
type Stats struct {
	Connections, Malformed, Other, Dropped, Gaps uint64
	Recent                                       []Gap
}

// Consumer owns a bounded queue and bounded gap history. Run must be called once.
// On queue overflow, counters/history preserve the loss even if notices cannot.
type Consumer struct {
	Notices   chan Notice
	Ready     chan struct{}
	source    Source
	mu        sync.Mutex
	stats     Stats
	active    io.ReadCloser
	observing bool
	ready     sync.Once
	backoff   time.Duration
}

func NewConsumer(source Source) *Consumer {
	return &Consumer{Notices: make(chan Notice, 256), Ready: make(chan struct{}), source: source, backoff: 250 * time.Millisecond}
}
func (c *Consumer) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Recent = append([]Gap(nil), s.Recent...)
	return s
}
func (c *Consumer) gap(reason string) {
	c.mu.Lock()
	c.stats.Gaps++
	g := Gap{time.Now(), reason, c.stats.Gaps}
	if len(c.stats.Recent) >= 64 {
		copy(c.stats.Recent, c.stats.Recent[1:])
		c.stats.Recent = c.stats.Recent[:63]
	}
	c.stats.Recent = append(c.stats.Recent, g)
	snapshot := Coverage{Status: "interrupted", Generation: c.stats.Gaps}
	if c.observing {
		snapshot.Status = "observing"
	}
	c.mu.Unlock()
	select {
	case c.Notices <- Notice{Gap: &g, Generation: g.Generation, Coverage: snapshot}:
	default:
	}
}
func (c *Consumer) emit(e Event) {
	c.mu.Lock()
	snapshot := Coverage{Status: "interrupted", Generation: c.stats.Gaps}
	if c.observing {
		snapshot.Status = "observing"
	}
	generation := c.stats.Gaps
	c.mu.Unlock()
	select {
	case c.Notices <- Notice{origin: c, Event: &e, Generation: generation, Coverage: snapshot}:
	default:
		c.mu.Lock()
		c.stats.Dropped++
		c.mu.Unlock()
		c.gap("queue_overflow")
	}
}

// readFrame bounds allocation and discards oversize frames through their newline.
func readFrame(r *bufio.Reader) ([]byte, bool, error) {
	var frame []byte
	oversize := false
	for {
		part, err := r.ReadSlice('\n')
		if !oversize && len(frame)+len(part) <= MaxFrame {
			frame = append(frame, part...)
		} else {
			oversize = true
			frame = nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(frame) > 0 {
			return frame, oversize, nil
		}
		return frame, oversize, err
	}
}
func (c *Consumer) Run(ctx context.Context) error {
	defer close(c.Notices)
	if ctx == nil || c.source == nil {
		return errors.New("invalid event source")
	}
	delay := c.backoff
	for ctx.Err() == nil {
		stream, err := c.source(ctx)
		if err == nil {
			c.mu.Lock()
			c.stats.Connections++
			c.active = stream
			c.mu.Unlock()
			// Close must interrupt a blocked Read, including while no events arrive.
			stopped := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					_ = stream.Close()
				case <-stopped:
				}
			}()
			reader := bufio.NewReaderSize(stream, 64*1024)
			for ctx.Err() == nil {
				frame, oversize, readErr := readFrame(reader)
				if oversize {
					c.mu.Lock()
					c.stats.Malformed++
					c.mu.Unlock()
					c.gap("oversized_frame")
				} else if len(frame) > 0 {
					event, kind, parseErr := Parse(frame)
					if parseErr != nil {
						c.mu.Lock()
						c.stats.Malformed++
						c.mu.Unlock()
						c.gap("malformed_frame")
					} else {
						if kind == "other" || kind == "loss" || (kind == "exec" && event.Process.ExecID != "") {
							c.mu.Lock()
							c.observing = true
							c.mu.Unlock()
							c.ready.Do(func() { close(c.Ready) })
						}
						switch kind {
						case "exec":
							c.emit(event)
						case "loss":
							c.gap("source_reported_loss")
						default:
							c.mu.Lock()
							c.stats.Other++
							c.mu.Unlock()
						}
						delay = c.backoff
					}
				}
				if readErr != nil {
					break
				}
			}
			close(stopped)
			_ = stream.Close()
			c.mu.Lock()
			c.active = nil
			c.observing = false
			c.mu.Unlock()
		}
		if ctx.Err() != nil {
			return nil
		}
		c.gap("stream_interrupted")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 4*time.Second {
			delay *= 2
		}
	}
	return nil
}

// Interrupt closes only this consumer's source. Run reports a gap and reconnects.
func (c *Consumer) Interrupt() {
	c.mu.Lock()
	stream := c.active
	c.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// Coverage returns counters even when queue pressure dropped a gap notice.
// Source creation alone is not proof of a subscribed stream: observing becomes
// true only after a recognized structured frame arrives on that connection.
func (c *Consumer) Coverage() Coverage {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := "interrupted"
	if c.observing {
		status = "observing"
	}
	return Coverage{Status: status, Generation: c.stats.Gaps}
}

// OwnsNotice identifies an exec delivered by this controller's consumer.
func (c *Consumer) OwnsNotice(n Notice) bool {
	return n.origin == c && n.Event != nil
}

// EffectiveCoverage combines the event's frozen snapshot with current counters.
// A queued event crossing a gap cannot claim current coverage. The latest gap
// generation remains visible even when the corresponding notification was lost.
func (c *Consumer) EffectiveCoverage(n Notice) Coverage {
	current := c.Coverage()
	result := n.Coverage
	if current.Generation != result.Generation || current.Status != "observing" {
		result.Status = "unknown"
	}
	if current.Generation > result.Generation {
		result.Generation = current.Generation
	}
	return result
}
