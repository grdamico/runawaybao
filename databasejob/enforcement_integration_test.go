//go:build integration

package databasejob_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/enforcement"
	"github.com/grdamico/runawaybao/openbao"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This wrapper privately counts every dispatch via the sole revoker.
type liveEnforcement struct {
	controller *enforcement.Controller
	revoker    *openbao.LeaseRevoker
	audit      *auditBuffer
	mu         sync.Mutex
	targets    []string
}

func (l *liveEnforcement) Revoke(ctx context.Context, lease string) error {
	l.mu.Lock()
	l.targets = append(l.targets, lease)
	l.mu.Unlock()
	return l.revoker.Revoke(ctx, lease)
}

func (l *liveEnforcement) verify(ctx context.Context, port uint16, a, b databasejob.Record, ca, cb openbao.DatabaseCredentials) bool {
	l.mu.Lock()
	exact := len(l.targets) == 1 && l.targets[0] == b.LeaseID && a.LeaseID != b.LeaseID
	l.mu.Unlock()
	if !exact || time.Until(b.IssuedAt.Add(b.TTL)) <= 5*time.Second {
		return false
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig("postgres://unused:unused@127.0.0.1/runawaybao?sslmode=disable&connect_timeout=3")
	if err != nil {
		return false
	}
	config.Port, config.User, config.Password, config.Fallbacks = port, cb.Username, cb.Password, nil
	conn, err := pgx.ConnectConfig(check, config)
	if conn != nil {
		_ = conn.Close(check)
	}
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "28P01" {
		return false
	}
	pg, err := databasejob.NewPostgreSQL("127.0.0.1", port)
	return err == nil && pg.Read(check, ca) == nil
}

func (l *liveEnforcement) checkAudit(t *testing.T, capture *capturingIssuer) {
	t.Helper()
	data := l.audit.snapshot()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for id, cred := range capture.credentials {
		for _, value := range []string{id, cred.Username, cred.Password} {
			if value != "" && bytes.Contains(data, []byte(value)) {
				t.Fatal("enforcement audit leaked private issuance information")
			}
		}
	}
	for _, value := range capture.redactionValues {
		if value != "" && bytes.Contains(data, []byte(value)) {
			t.Fatal("enforcement audit leaked private bootstrap information")
		}
	}
}
