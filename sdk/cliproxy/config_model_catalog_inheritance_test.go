//go:build unit

package cliproxy

import (
	"slices"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/registry"
)

func TestBuildConfigModelsInheritsCatalogProperties(t *testing.T) {
	const name = "muse-spark-1.3-contributor"
	catalog := registry.LookupStaticModelInfoByChannel(name, "meta")
	if catalog == nil {
		t.Fatalf("catalog has no meta model %s", name)
	}
	got := buildMetaConfigModels(&config.MetaKey{Models: []config.MetaModel{{Name: name}}})
	if len(got) != 1 {
		t.Fatalf("model count = %d, want 1", len(got))
	}
	model := got[0]
	if model.DisplayName != catalog.DisplayName || model.Description != catalog.Description {
		t.Errorf("names = %q, %q; want %q, %q", model.DisplayName, model.Description, catalog.DisplayName, catalog.Description)
	}
	if model.ContextLength != catalog.ContextLength || model.MaxCompletionTokens != catalog.MaxCompletionTokens {
		t.Errorf("limits = %d, %d; want %d, %d", model.ContextLength, model.MaxCompletionTokens, catalog.ContextLength, catalog.MaxCompletionTokens)
	}
	if !slices.Equal(model.SupportedInputModalities, catalog.SupportedInputModalities) {
		t.Errorf("input modalities = %v, want %v", model.SupportedInputModalities, catalog.SupportedInputModalities)
	}
	if model.Thinking == nil || !slices.Equal(model.Thinking.Levels, catalog.Thinking.Levels) {
		t.Errorf("thinking = %+v, want levels %v", model.Thinking, catalog.Thinking.Levels)
	}
}

func TestBuildConfigModelsKeepsConfiguredProperties(t *testing.T) {
	got := buildMetaConfigModels(&config.MetaKey{Models: []config.MetaModel{{
		Name: "muse-spark-1.3-contributor", DisplayName: "Configured", MaxContextLength: 200000,
	}}})[0]
	if got.DisplayName != "Configured" || got.ContextLength != 200000 {
		t.Fatalf("display name, context = %q, %d; want Configured, 200000", got.DisplayName, got.ContextLength)
	}
}
