//go:build integration

package schema_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/AsheTheWings/cliapiproxy/internal/generations"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/schema"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/pgtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var server *pgtest.Server

func TestMain(m *testing.M) { pgtest.Main(m, &server) }

// templateApplied records what the first Apply against an empty database did.
var (
	templateMu      sync.Mutex
	templateApplied []schema.Migration
)

func migrateTemplate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := schema.Discover(schema.Migrations())
	if err != nil {
		return err
	}
	applied, err := schema.Apply(ctx, pool, migrations)
	templateMu.Lock()
	templateApplied = applied
	templateMu.Unlock()
	return err
}

func migrated(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	name := server.Clone(t, "migrated", migrateTemplate)
	return name, server.Pool(t, name)
}

func TestEveryMigrationAppliesToAnEmptyDatabaseAndRerunsAsANoOp(t *testing.T) {
	_, pool := migrated(t)
	migrations, err := schema.Discover(schema.Migrations())
	if err != nil {
		t.Fatal(err)
	}
	templateMu.Lock()
	applied := len(templateApplied)
	templateMu.Unlock()
	if applied != len(migrations) {
		t.Fatalf("the empty database applied %d of %d migrations", applied, len(migrations))
	}
	ctx := context.Background()
	again, err := schema.Apply(ctx, pool, migrations)
	if err != nil || len(again) != 0 {
		t.Fatalf("rerun applied %v, %v; want nothing", again, err)
	}
	report, err := schema.Status(ctx, pool, migrations)
	if err != nil || !report.Clean() || len(report.Applied) != len(migrations) {
		t.Fatalf("status = %+v, %v", report, err)
	}
	for i, a := range report.Applied {
		if a.Name != migrations[i].Name || a.SHA256 != migrations[i].SHA256 {
			t.Errorf("ledger row %d = %+v, want %s", i, a, migrations[i].Name)
		}
	}
	if err = generations.CheckSchema(ctx, pool); err != nil {
		t.Fatalf("a migrated store is refused: %v", err)
	}
}

func TestServerRefusesAStoreWithPendingMigrations(t *testing.T) {
	pool := server.Pool(t, server.Database(t))
	err := generations.CheckSchema(context.Background(), pool)
	if err == nil || !strings.Contains(err.Error(), "pending 0001_") {
		t.Fatalf("an empty store: err = %v, want pending migrations", err)
	}
	// The check reads the ledger only and creates nothing.
	var ledger *string
	if errScan := pool.QueryRow(context.Background(), "SELECT to_regclass('public.schema_migrations')::text").Scan(&ledger); errScan != nil || ledger != nil {
		t.Fatalf("the check created the ledger: %v, %v", ledger, errScan)
	}
}

func synthetic(sql map[string]string) []schema.Migration {
	dir := fstest.MapFS{}
	for name, body := range sql {
		dir[name] = &fstest.MapFile{Data: []byte(body)}
	}
	migrations, err := schema.Discover(dir)
	if err != nil {
		panic(err)
	}
	return migrations
}

func TestRunnerRefusesDriftAndStopsAtAFailure(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{"0001_a.sql": "CREATE TABLE a (x int);", "0002_b.sql": "CREATE TABLE b (x int);"}
	for name, tc := range map[string]struct {
		next map[string]string
		want string
	}{
		"changed checksum":        {map[string]string{"0001_a.sql": base["0001_a.sql"], "0002_b.sql": "CREATE TABLE b (y int);"}, "changed after it was applied"},
		"position without a file": {map[string]string{"0001_a.sql": base["0001_a.sql"]}, "has no file"},
		"gap below applied":       {map[string]string{"0001_a.sql": base["0001_a.sql"], "0002_b.sql": base["0002_b.sql"], "0003_c.sql": "CREATE TABLE c (x int);", "0004_d.sql": "CREATE TABLE d (x int);"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			pool := server.Pool(t, server.Database(t))
			applyFirst := base
			if name == "gap below applied" {
				applyFirst = map[string]string{"0001_a.sql": base["0001_a.sql"], "0002_b.sql": base["0002_b.sql"], "0004_d.sql": "CREATE TABLE d (x int);"}
				tc.want = "missing from the ledger below applied position 4"
			}
			if _, err := schema.Apply(ctx, pool, synthetic(applyFirst)); err != nil {
				t.Fatal(err)
			}
			applied, err := schema.Apply(ctx, pool, synthetic(tc.next))
			if !errors.Is(err, schema.ErrDrift) || !strings.Contains(err.Error(), tc.want) || len(applied) != 0 {
				t.Fatalf("Apply = %v, %v; want drift naming %q and nothing applied", applied, err, tc.want)
			}
			report, err := schema.Status(ctx, pool, synthetic(tc.next))
			if err != nil || report.Clean() || len(report.Drifted) != 1 {
				t.Fatalf("Status = %+v, %v; want one drift", report, err)
			}
		})
	}
	t.Run("failing migration", func(t *testing.T) {
		pool := server.Pool(t, server.Database(t))
		applied, err := schema.Apply(ctx, pool, synthetic(map[string]string{
			"0001_a.sql": "CREATE TABLE a (x int);",
			"0002_b.sql": "CREATE TABLE b (x int); SELECT no_such_function();",
			"0003_c.sql": "CREATE TABLE c (x int);",
		}))
		if err == nil || len(applied) != 1 {
			t.Fatalf("Apply = %v, %v; want the first migration and an error", applied, err)
		}
		var tables []string
		rows, _ := pool.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY 1")
		tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || strings.Join(tables, ",") != "a,schema_migrations" {
			t.Fatalf("tables = %v, %v; want the failed migration rolled back", tables, err)
		}
		var recorded int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&recorded); err != nil || recorded != 1 {
			t.Fatalf("ledger rows = %d, %v; want only the applied migration", recorded, err)
		}
	})
	t.Run("duplicate position", func(t *testing.T) {
		pool := server.Pool(t, server.Database(t))
		if _, err := schema.Apply(ctx, pool, synthetic(base)); err != nil {
			t.Fatal(err)
		}
		_, err := schema.Discover(fstest.MapFS{
			"0001_a.sql":     &fstest.MapFile{Data: []byte(base["0001_a.sql"])},
			"0002_b.sql":     &fstest.MapFile{Data: []byte(base["0002_b.sql"])},
			"0003_c.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE c (x int);")},
			"0003_other.sql": &fstest.MapFile{Data: []byte("CREATE TABLE other (x int);")},
		})
		if err == nil || !strings.Contains(err.Error(), "share position 3") {
			t.Fatalf("Discover = %v; want the shared position refused", err)
		}
		var recorded int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&recorded); err != nil || recorded != 2 {
			t.Fatalf("ledger rows = %d, %v; want the store untouched", recorded, err)
		}
	})
}

// storeRelations lists every table, view, and materialized view of the store.
func storeRelations(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT c.relname, c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'v', 'm')`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		out[name] = kind
	}
	return out
}

func TestReaderRoleOnlyReads(t *testing.T) {
	name, owner := migrated(t)
	ctx := context.Background()
	relations := storeRelations(t, owner)
	for _, want := range []string{"generations", "generation_payloads", "system_instructions", "credential_usage_snapshots",
		"credential_usage_meters", "generation_usage_daily", "credential_usage_timeline", "schema_migrations"} {
		if _, ok := relations[want]; !ok {
			t.Fatalf("the store lacks %s: %v", want, relations)
		}
	}
	var attributes string
	if err := owner.QueryRow(ctx, `SELECT format('%s %s %s %s %s %s', rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls)
		FROM pg_roles WHERE rolname = 'cliapiproxy_reader'`).Scan(&attributes); err != nil || attributes != "t f f f f f" {
		t.Fatalf("reader attributes = %q, %v; want LOGIN and every NO attribute", attributes, err)
	}
	const password = "synthetic-reader-password"
	if _, err := owner.Exec(ctx, "ALTER ROLE cliapiproxy_reader PASSWORD '"+password+"'"); err != nil {
		t.Fatal(err)
	}
	reader, err := pgx.Connect(ctx, server.URL(name, "cliapiproxy_reader", password))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(ctx) }()
	refused := func(statement string) {
		t.Helper()
		if _, err := reader.Exec(ctx, statement); err == nil {
			t.Errorf("the reader ran %s", statement)
		}
	}
	for relation, kind := range relations {
		quoted := pgx.Identifier{relation}.Sanitize()
		if _, err = reader.Exec(ctx, "SELECT * FROM "+quoted+" LIMIT 1"); err != nil {
			t.Errorf("the reader cannot select %s: %v", relation, err)
		}
		refused("INSERT INTO " + quoted + " DEFAULT VALUES")
		refused("UPDATE " + quoted + " SET " + firstColumn(t, owner, relation) + " = NULL")
		refused("DELETE FROM " + quoted)
		refused("TRUNCATE " + quoted)
		switch kind {
		case "r":
			refused("ALTER TABLE " + quoted + " ADD COLUMN reader_column int")
			refused("DROP TABLE " + quoted)
		case "v":
			refused("DROP VIEW " + quoted)
		case "m":
			refused("REFRESH MATERIALIZED VIEW " + quoted)
			refused("DROP MATERIALIZED VIEW " + quoted)
		}
	}
	refused("CREATE TABLE reader_table (x int)")
	refused("CREATE ROLE reader_made")
}

func firstColumn(t *testing.T, pool *pgxpool.Pool, relation string) string {
	t.Helper()
	var column string
	if err := pool.QueryRow(context.Background(), `SELECT attname FROM pg_attribute
		WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped ORDER BY attnum LIMIT 1`, relation).Scan(&column); err != nil {
		t.Fatal(fmt.Errorf("first column of %s: %w", relation, err))
	}
	return pgx.Identifier{column}.Sanitize()
}
