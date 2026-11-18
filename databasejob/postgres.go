package databasejob

import (
	"context"
	"net"

	"github.com/grdamico/runawaybao/internal/dboperation"
	"github.com/grdamico/runawaybao/openbao"
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
	conn, err := dboperation.Connect(ctx, p.host, p.port, c.Username, c.Password)
	if err != nil {
		return ErrRead
	}
	defer dboperation.Close(conn)
	if dboperation.Read(ctx, conn) != nil {
		return ErrRead
	}
	return nil
}
