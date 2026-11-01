//go:build component

package keystate

import (
	"errors"
	"fmt"
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
	if _, err = store.ReplaceClientKeys([]upstream.Key{{APIKey: "client-key", Label: "ci"}}); err != nil {
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
	if groups := reopened.Groups(upstream.FamilyKiro); len(groups) != 1 || groups[0].Keys[0].APIKey != "state-key" || reopened.ClientKeys()[0] != (upstream.Key{APIKey: "client-key", Label: "ci"}) {
		t.Fatalf("reopened state = %+v, %v", groups, reopened.ClientKeys())
	}
}

// Every label the store accepts reads back from the file it writes, so a
// restart opens the state the management API saved.
func TestSavedLabelsReopenAfterARestart(t *testing.T) {
	labels := []string{"<<", "123", "true", "yes", "null", "~", "2026-01-01", "1.5", ".inf", "a: b", "- x", "#tag", "&anchor", "*alias", "!tag", "'quoted'", "\"double\"", "{x}", "[a]", "é team"}
	path := filepath.Join(t.TempDir(), "keys.yaml")
	store, err := New(path, []byte(inputsDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := make([]upstream.Key, len(labels))
	keys := make([]upstream.Key, len(labels))
	for i, label := range labels {
		clients[i] = upstream.Key{APIKey: fmt.Sprintf("client-%d", i), Label: label}
		keys[i] = upstream.Key{APIKey: fmt.Sprintf("upstream-%d", i), Label: label}
	}
	if _, err = store.ReplaceClientKeys(clients); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceGroups(upstream.FamilyKiro, []upstream.Group{{Spec: upstream.Spec{Name: "kiro"}, Keys: keys}}); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, []byte(inputsDocument), saved)
	if err != nil {
		t.Fatalf("reopen: %v\n%s", err, saved)
	}
	for i, k := range reopened.ClientKeys() {
		if k.Label != labels[i] || reopened.Groups(upstream.FamilyKiro)[0].Keys[i].Label != labels[i] {
			t.Errorf("label %q came back as %q", labels[i], k.Label)
		}
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
	if _, err = store.ReplaceClientKeys([]upstream.Key{{APIKey: "k"}, {APIKey: "k"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("repeated client key: err = %v, want ErrInvalid", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected change wrote the state file: %v", err)
	}
	if _, err = New(path, []byte(inputsDocument), []byte("api-keys: {kiro: [{name: k, keys: []}]}")); err == nil {
		t.Fatal("a broken state file opened")
	}
}
