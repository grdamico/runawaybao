package enforcement

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	ErrAuthorization = errors.New("local enforcement authorization denied")
	ErrIdentity      = errors.New("workload identity or lifecycle ineligible")
	ErrObservation   = errors.New("observation stale or duplicate")
	ErrCoverage      = errors.New("observation coverage uncertain")
)

type Revoker interface {
	Revoke(context.Context, string) error
}

// Target is produced by the trusted execution and issuance registry while
// both reservations are held. It is private and must never be logged.
type Target struct {
	Execution, Lease, PolicyDigest string
}

// Reservation supplies a recheck scoped to the exact reserved execution.
// It holds lifecycle and lease reservations until the callback returns.
type Reservation func(func(Target, func() bool) error) error

type Record struct {
	Run       string `json:"run"`
	Execution string `json:"execution,omitempty"`
	Event     string `json:"event,omitempty"`
	Operation string `json:"operation,omitempty"`
	Stage     string `json:"stage"`
	Reason    string `json:"reason"`
}

type Controller struct {
	mu      sync.Mutex
	run     string
	scope   string
	journal *journal
	revoker Revoker
	audit   func(Record)
	blocked bool
	busy    bool
	last    Record
}

func NewRun() (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		return "", ErrAuthorization
	}
	return hex.EncodeToString(id[:]), nil
}

// NewLocal is called only by trusted bootstrap after interactive confirmation
// and target verification. Run and target are controller-generated values.
func NewLocal(run, target, confirmation, path string, revoker Revoker, audit func(Record)) (*Controller, error) {
	if !validReference(run, 32) || target == "" || confirmation != target+" "+run || revoker == nil || audit == nil {
		return nil, ErrAuthorization
	}
	j, err := openJournal(path)
	if err != nil {
		return nil, err
	}
	return &Controller{run: run, scope: target, journal: j, revoker: revoker, audit: audit}, nil
}

func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy {
		return ErrJournal
	}
	c.blocked = true
	return c.journal.file.Close()
}

// Consider receives an opaque digest of the observation and a trusted
// reservation, never an event lease ID. A nil controller is dry-run.
func (c *Controller) Consider(ctx context.Context, event string, reserve Reservation) string {
	if c == nil {
		return "enforcement_disabled"
	}
	if !validReference(event, 64) || ctx == nil || reserve == nil {
		c.emit(Record{Run: c.run}, "enforcement_authorization", "authorization_denied")
		return "ineligible"
	}
	r := Record{Run: c.run, Event: event}
	c.emit(r, "observation", "observation_received")
	outcome := "ineligible"
	err := reserve(func(target Target, recheck func() bool) error {
		if !validReference(target.Execution, 32) || target.Lease == "" || !validReference(target.PolicyDigest, 64) {
			return ErrAuthorization
		}
		r.Execution = target.Execution
		c.emit(r, "correlation", "verified_identity")
		c.emit(r, "policy_evaluation", "suspicious_execution")
		op, err := NewRun()
		if err != nil {
			return err
		}
		r.Operation = op
		e := entry{Run: c.run, Scope: c.scope, Execution: r.Execution, Event: event, Operation: op, Lease: target.Lease, PolicyDigest: target.PolicyDigest}
		if ctx.Err() != nil || !recheck() {
			return ErrAuthorization
		}
		c.mu.Lock()
		if c.blocked || c.busy || c.journal.used[target.Lease] || c.journal.events[event] {
			c.mu.Unlock()
			return ErrAuthorization
		}
		if c.journal.append(e, "authorized") != nil {
			// Persistence errors and late cancellations both fail closed.
			c.blocked = true
			c.mu.Unlock()
			return ErrJournal
		}
		if c.journal.append(e, "dispatch_intent") != nil {
			c.blocked = true
			c.mu.Unlock()
			return ErrJournal
		}
		c.journal.used[target.Lease] = true
		c.journal.events[event] = true
		c.busy = true
		c.mu.Unlock()
		c.emit(r, "enforcement_authorization", "authorized_not_attempted")
		defer func() {
			c.mu.Lock()
			c.busy = false
			c.mu.Unlock()
		}()
		if !recheck() || ctx.Err() != nil {
			outcome = "authorized_not_attempted"
			c.mu.Lock()
			if c.journal.append(e, "not_attempted") != nil {
				c.blocked = true
				outcome = "revocation_outcome_uncertain"
			}
			c.mu.Unlock()
			c.emit(r, "enforcement_authorization", outcome)
			return nil
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		c.emit(r, "revocation_attempt", "attempt_in_progress")
		err = c.revoker.Revoke(call, target.Lease)
		cancel()
		stage := "acknowledged"
		outcome = "revocation_acknowledged"
		if err != nil {
			stage = "uncertain"
			outcome = "revocation_outcome_uncertain"
		}
		c.mu.Lock()
		c.last = r
		if c.journal.append(e, stage) != nil {
			c.blocked = true
			outcome = "revocation_outcome_uncertain"
		}
		c.mu.Unlock()
		c.emit(r, "revocation_acknowledgement", outcome)
		return nil
	})
	if err != nil {
		reason := "authorization_denied"
		switch {
		case errors.Is(err, ErrIdentity):
			reason = "identity_or_lifecycle_ineligible"
		case errors.Is(err, ErrObservation):
			reason = "stale_or_duplicate_observation"
		case errors.Is(err, ErrCoverage):
			reason = "coverage_uncertain"
		case errors.Is(err, ErrJournal):
			reason = "journal_unavailable"
		}
		c.emit(r, "enforcement_authorization", reason)
	}
	return outcome
}

func (c *Controller) emit(r Record, stage, reason string) {
	r.Stage, r.Reason = stage, reason
	c.audit(r)
}

func (c *Controller) Verification(success bool) {
	reason := "verification_failed_or_unavailable"
	if success {
		reason = "verification_succeeded"
	}
	c.mu.Lock()
	r := c.last
	c.mu.Unlock()
	c.emit(r, "database_verification", reason)
}

func validReference(ref string, size int) bool {
	if len(ref) != size {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}
