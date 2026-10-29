//go:build component

package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func kiroCatalogMetadata(modelIDs ...string) map[string]any {
	models := make([]any, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, map[string]any{
			"modelId":     id,
			"tokenLimits": map[string]any{"maxInputTokens": 200000, "maxOutputTokens": 64000},
		})
	}
	return map[string]any{"kiro_catalog": map[string]any{"models": models}}
}

func registeredKiroModels(clientID string) map[string]bool {
	out := make(map[string]bool)
	for _, model := range internalregistry.GetGlobalRegistry().GetModelsForClient(clientID) {
		out[model.ID] = true
	}
	return out
}

func TestKiroCredentialsPublishTheirOwnCatalogs(t *testing.T) {
	registry := internalregistry.GetGlobalRegistry()
	for _, id := range []string{"kiro-plan-a", "kiro-plan-b", "kiro-no-catalog"} {
		registry.UnregisterClient(id)
		t.Cleanup(func() { registry.UnregisterClient(id) })
	}
	service := &Service{cfg: &config.Config{}}
	register := func(id string, metadata map[string]any) {
		service.registerModelsForAuth(context.Background(), &coreauth.Auth{
			ID: id, Provider: "kiro", Status: coreauth.StatusActive, Metadata: metadata,
		})
	}
	register("kiro-plan-a", kiroCatalogMetadata("claude-haiku-5.5", "deepseek-3.2"))
	register("kiro-plan-b", kiroCatalogMetadata("deepseek-3.2", "kimi-k3"))
	register("kiro-no-catalog", map[string]any{"type": "kiro"})

	a, b := registeredKiroModels("kiro-plan-a"), registeredKiroModels("kiro-plan-b")
	if len(a) != 2 || !a["kiro/claude-haiku-5.5"] || !a["kiro/deepseek-3.2"] {
		t.Errorf("plan A models = %v", a)
	}
	if len(b) != 2 || !b["kiro/kimi-k3"] || b["kiro/claude-haiku-5.5"] {
		t.Errorf("plan B models = %v", b)
	}
	if none := registeredKiroModels("kiro-no-catalog"); len(none) != 0 {
		t.Errorf("a credential without a catalog published %v", none)
	}
	if registry.GetModelCount("kiro/claude-haiku-5.5") != 1 || registry.GetModelCount("kiro/deepseek-3.2") != 2 {
		t.Errorf("model counts: haiku=%d deepseek=%d", registry.GetModelCount("kiro/claude-haiku-5.5"), registry.GetModelCount("kiro/deepseek-3.2"))
	}

	// A new catalog replaces the old one.
	register("kiro-plan-a", kiroCatalogMetadata("kimi-k3"))
	if got := registeredKiroModels("kiro-plan-a"); len(got) != 1 || !got["kiro/kimi-k3"] {
		t.Errorf("plan A models after a catalog change = %v", got)
	}
}

func TestKiroExecutorIsRegisteredWithTheBaseline(t *testing.T) {
	found := false
	for _, auth := range baselineExecutorAuths() {
		if auth.Provider == "kiro" {
			found = true
		}
	}
	if !found {
		t.Error("the kiro provider is not among the baseline executors")
	}
}
