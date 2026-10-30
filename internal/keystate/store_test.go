//go:build component

package keystate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
)

const inputsDocument = "config-version: 8\noauth: {auth-dir: /var/lib/cliapiproxy/auths}\n"

func kiroGroup(name, key string) upstream.Group {
	return upstream.Group{Spec: upstream.Spec{Name: name}, Keys: []upstream.Key{{APIKey: key}}}
}

func TestReplaceWritesTheStateAndBuildsItsConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.yaml")
	store, err := New(path, []byte(inputsDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceClientKeys([]string{"client-key"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.ReplaceGroups(upstream.FamilyKiro, []upstream.Group{kiroGroup("kiro", "state-key")})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KiroKey) != 1 || cfg.KiroKey[0].APIKey != "state-key" || len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "client-key" || cfg.AuthDir != "/var/lib/cliapiproxy/auths" {
		t.Fatalf("configuration = kiro %+v, client keys %v, auth dir %s", cfg.KiroKey, cfg.APIKeys, cfg.AuthDir)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, []byte(inputsDocument), saved)
	if err != nil {
		t.Fatal(err)
	}
	if groups := reopened.Groups(upstream.FamilyKiro); len(groups) != 1 || groups[0].Keys[0].APIKey != "state-key" || reopened.ClientKeys()[0] != "client-key" {
		t.Fatalf("reopened state = %+v, %v", groups, reopened.ClientKeys())
	}
}

func TestReplaceRejectsAndKeepsTheState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.yaml")
	store, err := New(path, []byte(inputsDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, groups := range map[string][]upstream.Group{
		"no key":   {{Spec: upstream.Spec{Name: "cc"}}},
		"bad name": {kiroGroup("Bad Name", "k")},
	} {
		if _, err = store.ReplaceGroups(upstream.FamilyKiro, groups); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err = store.ReplaceClientKeys([]string{"k", "k"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("repeated client key: err = %v, want ErrInvalid", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected change wrote the state file: %v", err)
	}
	if _, err = New(path, []byte(inputsDocument), []byte("api-keys: {kiro: [{name: k, keys: []}]}")); err == nil {
		t.Fatal("a broken state file opened")
	}
}
