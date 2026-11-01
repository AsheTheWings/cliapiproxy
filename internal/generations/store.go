// Package generations keeps the durable record of the completed generations
// the server proxies, with their payloads and the usage readings of the
// credentials that served them, in PostgreSQL.
package generations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/schema"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings name the store's connection. An empty Host leaves the store off.
type Settings struct {
	// Host is a host name, or the directory of the store's Unix socket.
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

// Enabled reports whether the settings name a store.
func (s Settings) Enabled() bool { return strings.TrimSpace(s.Host) != "" }

// connectTimeout bounds establishing one connection.
const connectTimeout = 10 * time.Second

// Open connects a pool to the store. It reads no environment variable or
// password file: the settings are the connection's only inputs.
func Open(ctx context.Context, s Settings) (*pgxpool.Pool, error) {
	if !s.Enabled() {
		return nil, errors.New("the store is not configured: store.host is empty")
	}
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("store connection: %w", err)
	}
	conn := cfg.ConnConfig
	conn.Host = s.Host
	conn.Port = uint16(s.Port)
	conn.Database = s.Database
	conn.User = s.User
	conn.Password = s.Password
	conn.ConnectTimeout = connectTimeout
	// The store is reached over its Unix socket or the loopback interface,
	// so connections carry no TLS.
	conn.TLSConfig = nil
	conn.Fallbacks = nil
	conn.RuntimeParams = map[string]string{"application_name": "cliapiproxy"}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store connection: %w", err)
	}
	return pool, nil
}

// CheckSchema refuses a store whose ledger shows a pending or drifted
// migration; the server never applies migrations itself.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := schema.Discover(schema.Migrations())
	if err != nil {
		return err
	}
	report, err := schema.Status(ctx, pool, migrations)
	if err != nil {
		return err
	}
	if !report.Clean() {
		return fmt.Errorf("the store's schema does not match this build; run -migrate-status, then -migrate:\n%s", strings.Join(report.Lines()[len(report.Applied):], "\n"))
	}
	return nil
}
