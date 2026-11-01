// Package schema owns the PostgreSQL schema of the generations store: its
// ordered migrations, embedded in the binary, and the runner that applies
// them and reports their state. The runner is the only code that creates or
// alters the store's tables, views, and roles.
package schema

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations returns the embedded migration directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// LedgerTable records every applied migration.
const LedgerTable = "schema_migrations"

// advisoryLockKey serializes runners against one database.
const advisoryLockKey int64 = 0x636c6970726f7879 // "cliproxy"

// Migration is one SQL file of the directory, named <position>_<name>.sql.
type Migration struct {
	Position int
	Name     string
	SQL      string
	SHA256   string
}

var fileName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// Discover reads a migration directory in position order. It refuses a file
// whose name does not follow <4-digit position>_<name>.sql and two files that
// share a position.
func Discover(dir fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration directory holds a directory %s", entry.Name())
		}
		match := fileName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %s: want a name of a 4-digit position, an underscore, a name of lowercase letters, digits, and underscores, and .sql", entry.Name())
		}
		position, _ := strconv.Atoi(match[1])
		if position == 0 {
			return nil, fmt.Errorf("migration %s: positions start at 0001", entry.Name())
		}
		if other, taken := seen[position]; taken {
			return nil, fmt.Errorf("migrations %s and %s share position %d", other, entry.Name(), position)
		}
		seen[position] = entry.Name()
		data, err := fs.ReadFile(dir, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(data)
		out = append(out, Migration{Position: position, Name: strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())), SQL: string(data), SHA256: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(out, func(a, b Migration) int { return a.Position - b.Position })
	return out, nil
}

// Applied is one ledger row.
type Applied struct {
	Position int
	Name     string
	SHA256   string
}

// Report compares a migration directory with a database's ledger.
type Report struct {
	Applied []Applied
	Pending []Migration
	// Drifted lists applied migrations whose file changed, was renamed, or
	// whose position has no file, and files missing from the ledger below
	// the highest applied position.
	Drifted []string
}

// Clean reports a schema the server can run against: nothing pending or drifted.
func (r Report) Clean() bool { return len(r.Pending) == 0 && len(r.Drifted) == 0 }

// Lines renders the report, one migration per line.
func (r Report) Lines() []string {
	var out []string
	for _, a := range r.Applied {
		out = append(out, fmt.Sprintf("applied %s", a.Name))
	}
	for _, m := range r.Pending {
		out = append(out, fmt.Sprintf("pending %s", m.Name))
	}
	for _, d := range r.Drifted {
		out = append(out, "drifted "+d)
	}
	return out
}

// querier is what Status reads through.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Status reads the ledger and compares it with the migrations. It writes
// nothing; a database without a ledger has every migration pending.
func Status(ctx context.Context, db querier, migrations []Migration) (Report, error) {
	var ledger *string
	if err := db.QueryRow(ctx, "SELECT to_regclass($1)::text", "public."+LedgerTable).Scan(&ledger); err != nil {
		return Report{}, fmt.Errorf("read the migration ledger: %w", err)
	}
	var applied []Applied
	if ledger != nil {
		rows, err := db.Query(ctx, "SELECT position, name, sha256 FROM "+LedgerTable+" ORDER BY position")
		if err != nil {
			return Report{}, fmt.Errorf("read the migration ledger: %w", err)
		}
		applied, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Applied, error) {
			var a Applied
			err := row.Scan(&a.Position, &a.Name, &a.SHA256)
			return a, err
		})
		if err != nil {
			return Report{}, fmt.Errorf("read the migration ledger: %w", err)
		}
	}
	return compare(applied, migrations), nil
}

// compare classifies each migration and ledger row.
func compare(applied []Applied, migrations []Migration) Report {
	report := Report{Applied: applied}
	byPosition := map[int]Migration{}
	for _, m := range migrations {
		byPosition[m.Position] = m
	}
	recorded := map[int]bool{}
	highest := 0
	for _, a := range applied {
		recorded[a.Position] = true
		highest = max(highest, a.Position)
		m, ok := byPosition[a.Position]
		switch {
		case !ok:
			report.Drifted = append(report.Drifted, fmt.Sprintf("%s: applied at position %d, which has no file", a.Name, a.Position))
		case m.Name != a.Name:
			report.Drifted = append(report.Drifted, fmt.Sprintf("%s: applied at position %d, where the file is now %s", a.Name, a.Position, m.Name))
		case m.SHA256 != a.SHA256:
			report.Drifted = append(report.Drifted, fmt.Sprintf("%s: the file changed after it was applied", a.Name))
		}
	}
	for _, m := range migrations {
		if recorded[m.Position] {
			continue
		}
		if m.Position < highest {
			report.Drifted = append(report.Drifted, fmt.Sprintf("%s: missing from the ledger below applied position %d", m.Name, highest))
			continue
		}
		report.Pending = append(report.Pending, m)
	}
	return report
}

// ErrDrift marks a ledger that disagrees with the migration directory; the
// runner applies nothing until it is repaired.
var ErrDrift = errors.New("migration drift")

// Apply applies every pending migration in position order, each in its own
// transaction together with its ledger row, and returns those it applied.
// It refuses to apply anything when the ledger shows drift, and stops at the
// first failing migration without recording it. Concurrent runners against
// one database take turns.
func Apply(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) ([]Migration, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to the store: %w", err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("lock the migration ledger: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey) }()

	var ledger *string
	if err = conn.QueryRow(ctx, "SELECT to_regclass($1)::text", "public."+LedgerTable).Scan(&ledger); err != nil {
		return nil, fmt.Errorf("read the migration ledger: %w", err)
	}
	if ledger == nil {
		if _, err = conn.Exec(ctx, `CREATE TABLE public.`+LedgerTable+` (
	position integer PRIMARY KEY CHECK (position > 0),
	name text NOT NULL UNIQUE,
	sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
	applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
			return nil, fmt.Errorf("create the migration ledger: %w", err)
		}
	}
	report, err := Status(ctx, conn, migrations)
	if err != nil {
		return nil, err
	}
	if len(report.Drifted) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrDrift, strings.Join(report.Drifted, "; "))
	}
	var done []Migration
	for _, m := range report.Pending {
		if err = applyOne(ctx, conn.Conn(), m); err != nil {
			return done, err
		}
		done = append(done, m)
	}
	return done, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.Name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The simple protocol runs a file of several statements in one call.
	if _, err = tx.Conn().PgConn().Exec(ctx, m.SQL).ReadAll(); err != nil {
		return fmt.Errorf("migration %s: %w", m.Name, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO "+LedgerTable+" (position, name, sha256) VALUES ($1, $2, $3)", m.Position, m.Name, m.SHA256); err != nil {
		return fmt.Errorf("migration %s: record it in the ledger: %w", m.Name, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.Name, err)
	}
	return nil
}
