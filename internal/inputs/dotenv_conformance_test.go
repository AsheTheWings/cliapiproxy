//go:build conformance

package inputs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func moduleRoot(t *testing.T) string {
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
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestDotenvMatchesSchema(t *testing.T) {
	s, errLoad := Load()
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	tracked, errRead := os.ReadFile(filepath.Join(moduleRoot(t), ".env"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(tracked, s.RenderDotenv()) {
		t.Fatal(".env differs from the schema; run scripts/generate-env.sh")
	}
}
