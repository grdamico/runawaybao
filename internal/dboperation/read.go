// Package dboperation owns the single predefined PostgreSQL read. Routing is
// supplied by trusted code, not a job or stdin frame. Errors omit diagnostics.
package dboperation

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrDatabase = errors.New("database operation failed")

func Connect(ctx context.Context, host string, port uint16, user, password string) (*pgx.Conn, error) {
	c, err := pgx.ParseConfig("postgres://unused:unused@127.0.0.1:5432/runawaybao?sslmode=disable&connect_timeout=5&target_session_attrs=any")
	if err != nil {
		return nil, ErrDatabase
	}
	c.Host = host
	c.Port = port
	c.Database = "runawaybao"
	c.User = user
	c.Password = password
	c.TLSConfig = nil
	c.Fallbacks = nil
	c.Tracer = nil
	c.ValidateConnect = nil
	c.RuntimeParams = map[string]string{"statement_timeout": "5000", "application_name": "runawaybao-db-worker"}
	c.ConnectTimeout = 5 * time.Second
	conn, err := pgx.ConnectConfig(ctx, c)
	if err != nil {
		return nil, ErrDatabase
	}
	return conn, nil
}
func Read(ctx context.Context, c *pgx.Conn) error {
	var value string
	if c.QueryRow(ctx, "SELECT value FROM demo.fixture WHERE id = 1").Scan(&value) != nil || value != "demo-value" {
		return ErrDatabase
	}
	return nil
}
func Close(c *pgx.Conn) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Close(ctx)
}
