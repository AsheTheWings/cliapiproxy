//go:build unit

package schema

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// The embedded migrations discover cleanly and never hide drift behind
// existence guards.
func TestEmbeddedMigrationsUseNoExistenceGuards(t *testing.T) {
	migrations, err := Discover(Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	guard := regexp.MustCompile(`(?i)\bIF\s+(NOT\s+)?EXISTS\b`)
	for _, m := range migrations {
		if guard.MatchString(m.SQL) {
			t.Errorf("%s uses an IF [NOT] EXISTS guard", m.Name)
		}
	}
}

func files(names ...string) fstest.MapFS {
	dir := fstest.MapFS{}
	for _, name := range names {
		dir[name] = &fstest.MapFile{Data: []byte("SELECT 1; -- " + name)}
	}
	return dir
}

func TestDiscoverOrdersByPositionAndRefusesBadDirectories(t *testing.T) {
	got, err := Discover(files("0002_b.sql", "0010_c.sql", "0001_a.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
		if len(m.SHA256) != 64 {
			t.Errorf("%s: SHA-256 %q", m.Name, m.SHA256)
		}
	}
	if strings.Join(names, ",") != "0001_a,0002_b,0010_c" {
		t.Fatalf("order = %v", names)
	}
	for name, dir := range map[string]fstest.MapFS{
		"duplicate position": files("0001_a.sql", "0001_b.sql"),
		"position zero":      files("0000_a.sql"),
		"short position":     files("01_a.sql"),
		"uppercase name":     files("0001_A.sql"),
		"other extension":    files("0001_a.txt"),
		"subdirectory":       {"0001_a/x.sql": &fstest.MapFile{Data: []byte("x")}},
	} {
		if _, err = Discover(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCompareClassifiesPendingAndDrift(t *testing.T) {
	migrations, err := Discover(files("0001_a.sql", "0002_b.sql", "0003_c.sql"))
	if err != nil {
		t.Fatal(err)
	}
	ledger := func(ms ...Migration) []Applied {
		var out []Applied
		for _, m := range ms {
			out = append(out, Applied{Position: m.Position, Name: m.Name, SHA256: m.SHA256})
		}
		return out
	}
	if r := compare(nil, migrations); len(r.Pending) != 3 || len(r.Drifted) != 0 || r.Clean() {
		t.Fatalf("empty ledger = %+v", r)
	}
	if r := compare(ledger(migrations...), migrations); !r.Clean() {
		t.Fatalf("full ledger = %+v", r)
	}
	if r := compare(ledger(migrations[0]), migrations); len(r.Pending) != 2 || len(r.Drifted) != 0 {
		t.Fatalf("partial ledger = %+v", r)
	}
	changed := ledger(migrations...)
	changed[1].SHA256 = strings.Repeat("0", 64)
	renamed := ledger(migrations...)
	renamed[2].Name = "0003_old"
	unknown := append(ledger(migrations...), Applied{Position: 4, Name: "0004_d", SHA256: strings.Repeat("1", 64)})
	gap := ledger(migrations[0], migrations[2])
	for name, applied := range map[string][]Applied{"changed checksum": changed, "renamed file": renamed, "position without a file": unknown, "gap": gap} {
		r := compare(applied, migrations)
		if len(r.Drifted) != 1 || r.Clean() {
			t.Errorf("%s: %+v, want one drift", name, r)
		}
	}
}
