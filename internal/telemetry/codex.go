package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Attribute keys of the Codex client catalog span.
const (
	AttrCodexOptimizeMultiAgentV2    = attribute.Key("cliapiproxy.codex.optimize_multi_agent_v2")
	AttrCodexTemplateModels          = attribute.Key("cliapiproxy.codex.template_models")
	AttrCodexSynthesizedModels       = attribute.Key("cliapiproxy.codex.synthesized_models")
	AttrCodexSynthesizedMultiAgentV2 = attribute.Key("cliapiproxy.codex.synthesized_multi_agent_v2")
	AttrCodexFallbackTemplate        = attribute.Key("cliapiproxy.codex.fallback_template")
)

// CodexClientModelsReport describes how one Codex client catalog was built.
type CodexClientModelsReport struct {
	// TemplateModels counts models whose metadata came from their own template.
	TemplateModels int
	// SynthesizedModels counts models built from the fallback template.
	SynthesizedModels int
	// SynthesizedMultiAgentV2 counts synthesized models advertising multi-agent v2.
	SynthesizedMultiAgentV2 int
	// FallbackTemplate is the catalog slug synthesized models start from.
	FallbackTemplate string
}

// CodexClientModelsSpan is the span of one Codex client catalog build.
type CodexClientModelsSpan struct {
	span trace.Span
}

// StartCodexClientModels starts the span of a Codex client catalog build.
func StartCodexClientModels(ctx context.Context, optimizeMultiAgentV2 bool) (context.Context, CodexClientModelsSpan) {
	ctx, span := tracer().Start(ctx, "cliapiproxy.codex.client_models",
		trace.WithAttributes(AttrCodexOptimizeMultiAgentV2.Bool(optimizeMultiAgentV2)))
	return ctx, CodexClientModelsSpan{span: span}
}

// End records the report and ends the span.
func (s CodexClientModelsSpan) End(report CodexClientModelsReport) {
	s.span.SetAttributes(
		AttrCodexTemplateModels.Int(report.TemplateModels),
		AttrCodexSynthesizedModels.Int(report.SynthesizedModels),
		AttrCodexSynthesizedMultiAgentV2.Int(report.SynthesizedMultiAgentV2),
		AttrCodexFallbackTemplate.String(report.FallbackTemplate),
	)
	s.span.End()
}
