//go:build component

package openai

import (
	"context"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/registry"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/sdk/api/handlers"
	"go.opentelemetry.io/otel/attribute"
)

// The catalog span must describe the response it accompanies, so a wrong
// multi_agent_version traces to the template synthesized models came from.
func TestCodexClientModelsSpanDescribesTheCatalog(t *testing.T) {
	const modelID = "codex-client-telemetry-synthesized"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("codex-client-telemetry-client", "openai-compatibility", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("codex-client-telemetry-client") })
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)

	for _, optimize := range []bool{false, true} {
		rec := telemetrytest.Install(t)
		base.Cfg.Client.Codex.OptimizeMultiAgentV2 = optimize
		response := handler.codexClientModelsResponseContext(context.Background(), "")
		synthesizedV2 := 0
		for _, model := range response["models"].([]map[string]any) {
			if model["slug"] == modelID && model["multi_agent_version"] == "v2" {
				synthesizedV2++
			}
		}
		span := rec.Span(t, "cliapiproxy.codex.client_models")
		attr := func(key attribute.Key) int64 {
			value, _ := telemetrytest.Attr(span, key)
			return value.AsInt64()
		}
		if v, _ := telemetrytest.Attr(span, telemetry.AttrCodexOptimizeMultiAgentV2); v.AsBool() != optimize {
			t.Fatalf("optimize_multi_agent_v2 = %v, want %v", v.AsBool(), optimize)
		}
		if attr(telemetry.AttrCodexSynthesizedModels) < 1 {
			t.Fatal("the span does not count the synthesized model")
		}
		if got := attr(telemetry.AttrCodexSynthesizedMultiAgentV2); got < int64(synthesizedV2) {
			t.Fatalf("synthesized_multi_agent_v2 = %d, but the response advertises v2 on %d of the test's models", got, synthesizedV2)
		}
		if fallback, _ := telemetrytest.Attr(span, telemetry.AttrCodexFallbackTemplate); fallback.AsString() == "" {
			t.Fatal("the span does not name the fallback template")
		}
		rec.CheckCatalog(t)
	}
}
