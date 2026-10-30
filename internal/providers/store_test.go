//go:build component

package providers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
)

const inputsDocument = `config-version: 8
oauth: {auth-dir: /var/lib/cliapiproxy/auths}
api-keys:
  commandcode:
    - {name: inputs, keys: [{api-key: input-key}]}
`

func kiroGroup(name, key string) upstream.Group {
	return upstream.Group{Spec: upstream.Spec{Name: name}, Keys: []upstream.Key{{APIKey: key}}}
}

func TestReplaceWritesTheStateAndBuildsItsConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store, err := New(path, []byte(inputsDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Replace(upstream.FamilyKiro, []upstream.Group{kiroGroup("kiro", "state-key")})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KiroKey) != 1 || cfg.KiroKey[0].APIKey != "state-key" || len(cfg.CommandCodeKey) != 1 || cfg.AuthDir != "/var/lib/cliapiproxy/auths" {
		t.Fatalf("configuration = kiro %+v, commandcode %+v, auth dir %s", cfg.KiroKey, cfg.CommandCodeKey, cfg.AuthDir)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, []byte(inputsDocument), saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, state := reopened.Groups(upstream.FamilyKiro); len(state) != 1 || state[0].Keys[0].APIKey != "state-key" {
		t.Fatalf("reopened state = %+v", state)
	}
}

func TestReplaceRejectsAndKeepsTheState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store, err := New(path, []byte(inputsDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, groups := range map[string][]upstream.Group{
		"inputs name": {kiroGroup("inputs", "k")},
		"no key":      {{Spec: upstream.Spec{Name: "cc"}}},
		"bad name":    {kiroGroup("Bad Name", "k")},
	} {
		family := upstream.FamilyKiro
		if name == "inputs name" {
			family = upstream.FamilyCommandCode
		}
		if _, err = store.Replace(family, groups); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected change wrote the state file: %v", err)
	}
	if _, err = New(path, []byte(inputsDocument), []byte("api-keys: {kiro: [{name: k, keys: []}]}")); err == nil {
		t.Fatal("a broken state file opened")
	}
}
