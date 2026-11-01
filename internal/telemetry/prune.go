package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// Attribute keys of the pruning signals.
const (
	AttrPruneOutcome = attribute.Key("cliapiproxy.store.prune_outcome")
	AttrPayloadKind  = attribute.Key("cliapiproxy.payload.kind")
)

// Error classes of pruning failures.
const (
	ErrorResidualMismatch  = "residual_mismatch"
	ErrorMalformedResidual = "malformed_residual"
)

// StartPrunePass starts the span of one pruning pass; finish ends it with
// the pass's outcome class and, for a pass that failed in part or whole,
// the error class of its first failure, and records its duration.
func StartPrunePass(ctx context.Context) (context.Context, func(outcome, errorClass string)) {
	start := time.Now()
	ctx, span := tracer().Start(ctx, "cliapiproxy.store.prune")
	return ctx, func(outcome, errorClass string) {
		span.SetAttributes(AttrPruneOutcome.String(outcome))
		if errorClass != "" {
			span.SetAttributes(AttrErrorType.String(errorClass))
		}
		if outcome != "ok" {
			span.SetStatus(codes.Error, outcome)
		}
		span.End()
		if histogram, err := meter().Float64Histogram("cliapiproxy.store.prune.duration", metric.WithUnit("s"),
			metric.WithDescription("Duration of payload pruning passes, by outcome.")); err == nil {
			histogram.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(AttrPruneOutcome.String(outcome)))
		}
	}
}

// RecordPruned counts one pass's pruned inputs, the stored bytes they
// released, and the inputs it quarantined, for one payload kind.
func RecordPruned(ctx context.Context, kind string, pruned, released, quarantined int64) {
	m := meter()
	kindAttr := metric.WithAttributes(AttrPayloadKind.String(kind))
	if counter, err := m.Int64Counter("cliapiproxy.store.pruned", metric.WithUnit("{input}"),
		metric.WithDescription("Inputs replaced with residuals, by payload kind.")); err == nil {
		counter.Add(ctx, pruned, kindAttr)
	}
	if counter, err := m.Int64Counter("cliapiproxy.store.pruned.bytes", metric.WithUnit("By"),
		metric.WithDescription("Stored payload bytes that pruning released, by payload kind.")); err == nil {
		counter.Add(ctx, max(0, released), kindAttr)
	}
	if counter, err := m.Int64Counter("cliapiproxy.store.quarantined", metric.WithUnit("{input}"),
		metric.WithDescription("Inputs left full after repeated failed evaluations, by payload kind.")); err == nil {
		counter.Add(ctx, quarantined, kindAttr)
	}
}
