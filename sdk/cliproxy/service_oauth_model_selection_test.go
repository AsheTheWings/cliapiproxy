//go:build component

package cliproxy

import (
	"context"
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestOAuthModelsListTheCatalogModelsAProviderServes(t *testing.T) {
	catalog := internalregistry.GetCodexProModels()
	if len(catalog) < 2 {
		t.Fatalf("the Codex Pro catalog has %d models; the test needs two", len(catalog))
	}
	listed := catalog[1]
	registry := GlobalModelRegistry()
	for _, id := range []string{"codex-listed", "codex-unlisted"} {
		registry.UnregisterClient(id)
		t.Cleanup(func() { registry.UnregisterClient(id) })
	}
	register := func(id string, cfg *config.Config) {
		service := &Service{cfg: cfg}
		service.registerModelsForAuth(context.Background(), &coreauth.Auth{
			ID: id, Provider: "codex", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"},
		})
	}
	// Names match without regard to case.
	register("codex-listed", &config.Config{OAuthModels: map[string][]string{"codex": {strings.ToUpper(listed.ID)}}})
	register("codex-unlisted", &config.Config{OAuthModels: map[string][]string{"claude": {"claude-x"}}})

	got := registry.GetModelsForClient("codex-listed")
	if len(got) != 1 || got[0].ID != listed.ID || got[0].DisplayName != listed.DisplayName {
		t.Errorf("models = %+v, want only the catalog entry %s", got, listed.ID)
	}
	if got := registry.GetModelsForClient("codex-unlisted"); len(got) != len(catalog) {
		t.Errorf("a provider without a list serves %d of %d catalog models", len(got), len(catalog))
	}
}

func TestOAuthModelsApplyToEveryKimiSpelling(t *testing.T) {
	catalog := internalregistry.GetKimiModels()
	if len(catalog) < 2 {
		t.Fatalf("the Kimi catalog has %d models; the test needs two", len(catalog))
	}
	listed := catalog[0].ID
	registry := GlobalModelRegistry()
	// Config loading folds the domain spellings into the canonical keys.
	cfg := &config.Config{OAuthModels: map[string][]string{"kimi-ai": {listed}, "kimi": {listed}}}
	for _, provider := range []string{"kimi.ai", "kimi.com", "kimi-ai", "kimi"} {
		id := "kimi-selection-" + provider
		registry.UnregisterClient(id)
		t.Cleanup(func() { registry.UnregisterClient(id) })
		service := &Service{cfg: cfg}
		service.registerModelsForAuth(context.Background(), &coreauth.Auth{
			ID: id, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"},
		})
		if got := registry.GetModelsForClient(id); len(got) != 1 || got[0].ID != listed {
			t.Errorf("provider %s models = %+v, want only %s", provider, got, listed)
		}
	}
}

func TestOAuthModelsApplyToAntigravityCatalogHints(t *testing.T) {
	hinted := map[string]struct{}{}
	var listed string
	for _, model := range internalregistry.GetAntigravityModels() {
		if len(hinted) == 2 {
			break
		}
		id := normalizeAntigravityFetchedModelID(model.ID)
		hinted[id] = struct{}{}
		listed = model.ID
	}
	if len(hinted) < 2 {
		t.Fatalf("the Antigravity catalog has %d models; the test needs two", len(hinted))
	}
	service := &Service{}
	auth := &coreauth.Auth{ID: "antigravity-selection", Provider: "antigravity", Attributes: map[string]string{"auth_kind": "oauth"}}
	got := service.antigravityModelsForHintsWithConfig(auth, antigravityModelCapabilityHints{ModelIDs: hinted, revision: 1},
		&config.Config{OAuthModels: map[string][]string{"antigravity": {listed}}})
	if len(got) != 1 || got[0].ID != listed {
		t.Errorf("models = %+v, want only %s", got, listed)
	}
}
