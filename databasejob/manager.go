// Package databasejob owns credentials and explicit per-job lease lifecycle.
package databasejob

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/grdamico/runawaybao/openbao"
)

var (
	ErrConfiguration = errors.New("invalid database job configuration")
	ErrDuplicate     = errors.New("duplicate database job ID")
	ErrNotFound      = errors.New("database job not found")
	ErrBusy          = errors.New("database job operation in progress")
	ErrIssue         = errors.New("database credential issuance failed")
	ErrRead          = errors.New("database read failed")
	ErrRevoke        = errors.New("database lease revocation unconfirmed")
	ErrCleanup       = errors.New("database job failed and cleanup unconfirmed")
	ErrInactive      = errors.New("database job is not active")
	ErrClosed        = errors.New("database manager closed")
)

type Issuer interface {
	Issue(context.Context) (openbao.DatabaseCredentials, error)
}
type Revoker interface {
	Revoke(context.Context, string) error
}
type Reader interface {
	Read(context.Context, openbao.DatabaseCredentials) error
}

type State string

const (
	Issuing          State = "issuing"
	Active           State = "active"
	IssuanceFailed   State = "issuance_failed"
	Revoking         State = "revoking"
	RevocationFailed State = "revocation_failed"
	Revoked          State = "revoked"
)

// Record is controller-only metadata. WorkloadID identifies a trusted execution
// container; it is set by controller binding, never by a job input.
// TTL is an estimate, not proof that backend revocation has completed.
type Record struct {
	JobID      string
	LeaseID    string
	WorkloadID string
	IssuedAt   time.Time
	TTL        time.Duration
	State      State
	Delivery   string
}
type entry struct {
	record      Record
	credentials openbao.DatabaseCredentials
	busy        bool
}

// Manager must not be copied. Dependencies must support concurrent calls.
// Its mutex protects registry changes only, never network operations.
type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*entry
	issuer  Issuer
	revoker Revoker
	reader  Reader
	closed  bool
}

func New(issuer Issuer, revoker Revoker, reader Reader) (*Manager, error) {
	if issuer == nil || revoker == nil || reader == nil {
		return nil, ErrConfiguration
	}
	return &Manager{jobs: make(map[string]*entry), issuer: issuer, revoker: revoker, reader: reader}, nil
}
func validContext(ctx context.Context) bool { return ctx != nil && ctx.Err() == nil }
func (m *Manager) Execute(ctx context.Context, id string) error {
	if !validContext(ctx) || strings.TrimSpace(id) == "" {
		return ErrConfiguration
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if _, ok := m.jobs[id]; ok {
		m.mu.Unlock()
		return ErrDuplicate
	}
	e := &entry{record: Record{JobID: id, State: Issuing}, busy: true}
	m.jobs[id] = e
	m.mu.Unlock()
	defer m.release(e)
	credentials, err := m.issuer.Issue(ctx)
	if err == nil && (credentials.LeaseID == "" || credentials.Username == "" || credentials.Password == "" || credentials.TTL <= 0) {
		err = ErrIssue
	}
	m.mu.Lock()
	e.credentials = credentials
	e.record.LeaseID = credentials.LeaseID
	e.record.IssuedAt = time.Now()
	e.record.TTL = credentials.TTL
	if err != nil {
		e.record.State = IssuanceFailed
	} else {
		e.record.State = Active
	}
	m.mu.Unlock()
	if err != nil {
		if credentials.LeaseID != "" && m.cleanup(e) != nil {
			return ErrCleanup
		}
		return ErrIssue
	}
	if m.reader == nil || ctx.Err() != nil || m.reader.Read(ctx, credentials) != nil || ctx.Err() != nil {
		if m.cleanup(e) != nil {
			return ErrCleanup
		}
		return ErrRead
	}
	return nil
}
func (m *Manager) release(e *entry) { m.mu.Lock(); e.busy = false; m.mu.Unlock() }
func (m *Manager) acquire(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if e.busy {
		return nil, ErrBusy
	}
	e.busy = true
	return e, nil
}

// Read repeats only the fixed operation using this job's existing credentials.
// Every operation creates a fresh connection, including after a lease expires.
func (m *Manager) Read(ctx context.Context, id string) error {
	if !validContext(ctx) {
		return ErrConfiguration
	}
	e, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer m.release(e)
	m.mu.Lock()
	state := e.record.State
	credentials := e.credentials
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if state != Active {
		return ErrInactive
	}
	if m.reader == nil || m.reader.Read(ctx, credentials) != nil || ctx.Err() != nil {
		if m.cleanup(e) != nil {
			return ErrCleanup
		}
		return ErrRead
	}
	return nil
}
func (m *Manager) cleanup(e *entry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.revoke(ctx, e)
}
func (m *Manager) revoke(ctx context.Context, e *entry) error {
	m.mu.Lock()
	if e.record.State == Revoked {
		m.mu.Unlock()
		return nil
	}
	id := e.record.LeaseID
	if id == "" {
		m.mu.Unlock()
		return ErrInactive
	}
	e.record.State = Revoking
	m.mu.Unlock()
	var err error
	if m.revoker == nil {
		err = ErrRevoke
	} else {
		err = m.revoker.Revoke(ctx, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		e.record.State = RevocationFailed
		return ErrRevoke
	}
	e.record.State = Revoked
	e.credentials = openbao.DatabaseCredentials{}
	return nil
}

// RevokeJob is a trusted controller operation, never an untrusted job action.
// Repeated confirmed revocations are no-ops; failures remain retryable. Local
// expiry never implies success: expired leases still go through the backend.
func (m *Manager) RevokeJob(ctx context.Context, id string) error {
	if !validContext(ctx) {
		return ErrConfiguration
	}
	if m.revoker == nil {
		return ErrRevoke
	}
	e, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer m.release(e)
	return m.revoke(ctx, e)
}

// WithWorkloadLease reserves the exact private issuance record for a running
// workload. Workload managers have no legacy revocation client of their own.
func (m *Manager) WithWorkloadLease(job, workload string, use func(Record)) bool {
	if use == nil {
		return false
	}
	e, err := m.acquire(job)
	if err != nil {
		return false
	}
	defer m.release(e)
	m.mu.Lock()
	record := e.record
	valid := !m.closed && m.revoker == nil && record.WorkloadID == workload &&
		record.LeaseID != "" && record.State == Active && record.Delivery == "delivered"
	for _, other := range m.jobs {
		if other != e && other.record.LeaseID == record.LeaseID {
			valid = false
			break
		}
	}
	m.mu.Unlock()
	if valid {
		use(record)
	}
	return valid
}

// WorkloadLeaseCurrent is a final snapshot while WithWorkloadLease holds the
// entry reservation. Pending manager shutdown invalidates dispatch.
func (m *Manager) WorkloadLeaseCurrent(record Record) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.jobs[record.JobID]
	return !m.closed && e != nil && e.busy && e.record == record
}

func (m *Manager) Lookup(id string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return Record{}, false
	}
	return e.record, true
}

// Close rejects new execution and attempts each known lease, even after another
// cleanup fails. Call again to retry failed or busy entries; no prefix revocation.
// Callers should cancel and join active Execute/Read calls before shutdown.
func (m *Manager) Close(ctx context.Context) error {
	if !validContext(ctx) {
		return ErrConfiguration
	}
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.jobs))
	for id, e := range m.jobs {
		if e.record.LeaseID != "" || e.busy {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	var failed bool
	for _, id := range ids {
		if m.RevokeJob(ctx, id) != nil {
			failed = true
		}
	}
	if failed {
		return ErrCleanup
	}
	return nil
}

// BindWorkload attaches trusted runtime identity exactly once. It neither reads
// credentials nor changes lease state. A job cannot supply this metadata.
func (m *Manager) BindWorkload(jobID, workloadID string) error {
	if len(workloadID) != 64 {
		return ErrConfiguration
	}
	for _, r := range workloadID {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return ErrConfiguration
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	if e.busy {
		return ErrBusy
	}
	if m.closed || e.record.State != Active {
		return ErrInactive
	}
	if e.record.WorkloadID != "" {
		return ErrDuplicate
	}
	for _, other := range m.jobs {
		if other.record.WorkloadID == workloadID {
			return ErrDuplicate
		}
	}
	e.record.WorkloadID = workloadID
	return nil
}

// NewForWorkloads enables preparation/delivery only, with no revoker or host reader.
func NewForWorkloads(issuer Issuer) (*Manager, error) {
	if issuer == nil {
		return nil, ErrConfiguration
	}
	return &Manager{issuer: issuer, jobs: make(map[string]*entry)}, nil
}

// Prepare reserves and issues without opening PostgreSQL in the controller.
// Unknown issuance outcomes stay recorded; no automatic revocation is attempted.
func (m *Manager) Prepare(ctx context.Context, id string) error {
	if !validContext(ctx) || strings.TrimSpace(id) == "" {
		return ErrConfiguration
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if _, ok := m.jobs[id]; ok {
		m.mu.Unlock()
		return ErrDuplicate
	}
	e := &entry{record: Record{JobID: id, State: Issuing}, busy: true}
	m.jobs[id] = e
	m.mu.Unlock()
	defer m.release(e)
	issued := time.Now()
	credentials, err := m.issuer.Issue(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	e.credentials = credentials
	e.record.LeaseID = credentials.LeaseID
	e.record.TTL = credentials.TTL
	e.record.IssuedAt = issued
	e.record.Delivery = "not_delivered"
	if err != nil || ctx.Err() != nil || credentials.LeaseID == "" || credentials.Username == "" || credentials.Password == "" || credentials.TTL <= 0 {
		e.record.State = IssuanceFailed
		return ErrIssue
	}
	e.record.State = Active
	return nil
}

// Deliver invokes one trusted sink outside the registry lock. A failed or
// canceled attempt is ambiguous and cannot be automatically resent to a worker.
func (m *Manager) Deliver(ctx context.Context, id string, sink func(context.Context, string, string) error) error {
	if !validContext(ctx) || sink == nil {
		return ErrConfiguration
	}
	e, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer m.release(e)
	m.mu.Lock()
	if m.closed || e.record.State != Active || e.record.WorkloadID == "" || e.record.Delivery != "not_delivered" {
		m.mu.Unlock()
		return ErrInactive
	}
	e.record.Delivery = "delivering"
	c := e.credentials
	m.mu.Unlock()
	err = sink(ctx, c.Username, c.Password)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil || ctx.Err() != nil {
		e.record.Delivery = "uncertain"
		return ErrRead
	}
	e.record.Delivery = "delivered"
	return nil
}
