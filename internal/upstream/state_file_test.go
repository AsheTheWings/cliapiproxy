//go:build component

package upstream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteStateFileReplacesTheFileForItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "providers.yaml")
	for _, key := range []string{"k1", "k2"} {
		state := State{FamilyKiro: {{Spec: Spec{Name: "k"}, Keys: []Key{{APIKey: key}}}}}
		if err := WriteStateFile(path, state); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseState(data)
		if err != nil || parsed[FamilyKiro][0].Keys[0].APIKey != key {
			t.Fatalf("file holds %q (%v), want key %s", data, err, key)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v (%v), want 0600", info.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the state file", len(entries))
	}
}
