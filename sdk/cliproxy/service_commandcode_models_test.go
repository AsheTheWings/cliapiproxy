//go:build component

package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/AsheTheWings/cliapiproxy/internal/registry"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"github.com/AsheTheWings/cliapiproxy/sdk/config"
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

func TestCommandCodeGroupModelsSelectFromTheAccountCatalog(t *testing.T) {
	registry := internalregistry.GetGlobalRegistry()
	const id = "commandcode-listed"
	registry.UnregisterClient(id)
	t.Cleanup(func() { registry.UnregisterClient(id) })
	service := &Service{cfg: &config.Config{CommandCodeKey: []config.CommandCodeKey{{APIKey: "key", Models: []config.MetaModel{
		{Name: "commandcode/gpt-6-luna", Alias: "luna", MaxContextLength: 1000},
		{Name: "commandcode/deepseek/deepseek-v4-flash"},
		{Name: "commandcode/not-in-the-catalog"},
	}}}}}
	auth := &coreauth.Auth{
		ID: id, Provider: "commandcode", Status: coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeSource: "config:commandcode[listed]", coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeAPIKey: "key", coreauth.AttributeAccountCatalog: "true",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	if got := registeredCommandCodeModels(id); len(got) != 0 {
		t.Fatalf("a credential without a catalog published %v", got)
	}

	auth.Metadata = commandCodeCatalogMetadata("individual-pro", 0)
	service.registerModelsForAuth(context.Background(), auth)
	got := make(map[string]*internalregistry.ModelInfo)
	for _, model := range registry.GetModelsForClient(id) {
		got[model.ID] = model
	}
	if len(got) != 2 || got["luna"] == nil || got["commandcode/deepseek/deepseek-v4-flash"] == nil {
		t.Fatalf("models = %v, want the listed catalog models", got)
	}
	if luna := got["luna"]; luna.ContextLength != 1000 || luna.DisplayName != "gpt-6-luna" || luna.OwnedBy != "commandcode" {
		t.Errorf("luna = %+v, want the catalog entry with the group's alias and context", luna)
	}
}
