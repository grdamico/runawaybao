package databasejob

import (
	"context"
	"net"
	"time"

	"github.com/grdamico/runawaybao/openbao"
	"github.com/jackc/pgx/v5"
)

// PostgreSQL reads one fixture row. Host and port come from trusted setup, never
// job input. This client is deliberately local-lab only, with TLS disabled.
type PostgreSQL struct {
	host string
	port uint16
}

func NewPostgreSQL(host string, port uint16) (*PostgreSQL, error) {
	if net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == 0 {
		return nil, ErrConfiguration
	}
	return &PostgreSQL{host, port}, nil
}
func (p *PostgreSQL) Read(ctx context.Context, c openbao.DatabaseCredentials) error {
	if p == nil || !validContext(ctx) || c.Username == "" || c.Password == "" {
		return ErrConfiguration
	}
	// Parse a constant, then override all routing/auth fields. No credential-bearing
	// URI is built. Service-file parse failures remain safe configuration failures.
	config, err := pgx.ParseConfig("postgres://unused:unused@127.0.0.1:5432/runawaybao?sslmode=disable&connect_timeout=5&target_session_attrs=any")
	if err != nil {
		return ErrConfiguration
	}
	config.Host = p.host
	config.Port = p.port
	config.Database = "runawaybao"
	config.User = c.Username
	config.Password = c.Password
	config.TLSConfig = nil
	config.Fallbacks = nil
	config.ConnectTimeout = 5 * time.Second
	config.Tracer = nil
	config.RuntimeParams = map[string]string{"statement_timeout": "5000"}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return ErrRead
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var value string
	if err := conn.QueryRow(ctx, "SELECT value FROM demo.fixture WHERE id = 1").Scan(&value); err != nil || value != "demo-value" {
		return ErrRead
	}
	return nil
}
