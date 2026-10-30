//go:build unit

package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCommandCodeKeysBecomeAccountCatalogCredentials(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{CommandCodeKey: []config.CommandCodeKey{
			{APIKey: "key-a", Prefix: "cc"}, {APIKey: "key-b"}, {APIKey: " "},
		}},
		Now: now, IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 2 {
		t.Fatalf("auths = %+v", auths)
	}
	a := auths[0]
	if a.Provider != "commandcode" || a.Prefix != "cc" || a.ID == auths[1].ID {
		t.Fatalf("credential = %+v", a)
	}
	if kind, key := a.AccountInfo(); kind != "api_key" || key != "key-a" {
		t.Errorf("account = %s %q", kind, key)
	}
	if !coreauth.IsConfigAPIKeyAuth(a) || !coreauth.IsAccountCatalogAuth(a) {
		t.Errorf("attributes = %v; want a config API-key credential that serves its account's catalog", a.Attributes)
	}
	if _, hasExpiry := a.ExpirationTime(); hasExpiry || !a.LastRefreshedAt.IsZero() {
		t.Errorf("a new credential carries refresh state: %+v", a)
	}
	if _, hasCatalog := a.Metadata["commandcode_catalog"]; hasCatalog {
		t.Error("a new credential holds a catalog before its first reload")
	}
}
