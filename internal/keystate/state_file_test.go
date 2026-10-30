//go:build component

package keystate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesTheFileForItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "keys.yaml")
	for _, key := range []string{"k1", "k2"} {
		if err := WriteFile(path, State{ClientKeys: []string{key}}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(data)
		if err != nil || len(parsed.ClientKeys) != 1 || parsed.ClientKeys[0] != key {
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
