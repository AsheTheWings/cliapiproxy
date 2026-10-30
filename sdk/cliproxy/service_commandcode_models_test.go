//go:build component

package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func commandCodeCatalogMetadata(plan string, credits float64) map[string]any {
	return map[string]any{"commandcode_catalog": map[string]any{
		"account": map[string]any{"plan_id": plan, "purchased_credits": credits},
		"models": []any{
			map[string]any{"id": "deepseek/deepseek-v4-flash", "category": "opensource", "minimumPlan": "individual-go"},
			map[string]any{"id": "gpt-6-luna", "category": "premium", "minimumPlan": "individual-pro"},
		},
	}}
}

func registeredCommandCodeModels(clientID string) map[string]bool {
	out := make(map[string]bool)
	for _, model := range internalregistry.GetGlobalRegistry().GetModelsForClient(clientID) {
		out[model.ID] = true
	}
	return out
}

func TestCommandCodeCredentialsPublishOnlyTheModelsOfTheirPlan(t *testing.T) {
	registry := internalregistry.GetGlobalRegistry()
	for _, id := range []string{"commandcode-go", "commandcode-pro", "commandcode-credits", "commandcode-no-catalog"} {
		registry.UnregisterClient(id)
		t.Cleanup(func() { registry.UnregisterClient(id) })
	}
	service := &Service{cfg: &config.Config{}}
	register := func(id string, metadata map[string]any) {
		service.registerModelsForAuth(context.Background(), &coreauth.Auth{
			ID: id, Provider: "commandcode", Status: coreauth.StatusActive, Metadata: metadata,
		})
	}
	register("commandcode-go", commandCodeCatalogMetadata("individual-go", 0))
	register("commandcode-pro", commandCodeCatalogMetadata("individual-pro", 0))
	register("commandcode-credits", commandCodeCatalogMetadata("individual-go", 5))
	register("commandcode-no-catalog", map[string]any{"type": "commandcode"})

	if got := registeredCommandCodeModels("commandcode-go"); len(got) != 1 || !got["commandcode/deepseek/deepseek-v4-flash"] {
		t.Errorf("Go plan models = %v", got)
	}
	if got := registeredCommandCodeModels("commandcode-pro"); len(got) != 2 || !got["commandcode/gpt-6-luna"] {
		t.Errorf("Pro plan models = %v", got)
	}
	if got := registeredCommandCodeModels("commandcode-credits"); len(got) != 2 {
		t.Errorf("an account with credits has models %v", got)
	}
	if got := registeredCommandCodeModels("commandcode-no-catalog"); len(got) != 0 {
		t.Errorf("a credential without a catalog published %v", got)
	}
}

func TestCommandCodeExecutorIsRegisteredWithTheBaseline(t *testing.T) {
	for _, auth := range baselineExecutorAuths() {
		if auth.Provider == "commandcode" {
			return
		}
	}
	t.Error("the commandcode provider is not among the baseline executors")
}
