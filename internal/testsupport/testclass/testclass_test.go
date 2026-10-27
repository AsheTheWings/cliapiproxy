//go:build conformance

package testclass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repositoryRoot returns the directory holding go.mod above the working
// directory, which go test sets to this package's directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, errWd := os.Getwd()
	if errWd != nil {
		t.Fatal(errWd)
	}
	for {
		if _, errStat := os.Stat(filepath.Join(dir, "go.mod")); errStat == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func TestEveryTestFileDeclaresOneClassCoveringItsUse(t *testing.T) {
	files, errAnalyze := Analyze(repositoryRoot(t))
	if errAnalyze != nil {
		t.Fatal(errAnalyze)
	}
	for _, f := range files {
		if f.Tests == 0 {
			continue
		}
		if len(f.Declared) != 1 {
			t.Errorf("%s: declares classes %v, want exactly one; it requires %s (%s)",
				f.Path, f.Declared, f.Required(), strings.Join(f.Evidence(), "; "))
			continue
		}
		if !f.Declared[0].Covers(f.Required()) {
			t.Errorf("%s: declares %s, but it requires %s (%s)",
				f.Path, f.Declared[0], f.Required(), strings.Join(f.Evidence(), "; "))
		}
	}
}

func TestTestsUseNoFixedSleeps(t *testing.T) {
	files, errAnalyze := Analyze(repositoryRoot(t))
	if errAnalyze != nil {
		t.Fatal(errAnalyze)
	}
	for _, f := range files {
		for _, sleep := range f.Sleeps {
			t.Errorf("%s: time.Sleep outside a synctest bubble; wait on an observable condition, or mark a fake's "+
				"latency that must outlast a real deadline with %q", sleep, FixtureLatency)
		}
	}
}
