package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// Attribute keys of the usage poll signals.
const (
	AttrPollOutcome = attribute.Key("cliapiproxy.usage.poll_outcome")
	AttrMeterClass  = attribute.Key("cliapiproxy.usage.meter")
)

// StartUsagePoll starts the span of one usage poll; finish ends it with the
// poll's outcome class and records its duration.
func StartUsagePoll(ctx context.Context, provider string) (context.Context, func(outcome string)) {
	start := time.Now()
	ctx, span := tracer().Start(ctx, "cliapiproxy.usage.poll")
	return ctx, func(outcome string) {
		attrs := []attribute.KeyValue{AttrProvider.String(provider), AttrPollOutcome.String(outcome)}
		span.SetAttributes(attrs...)
		if outcome != "ok" && outcome != "unchanged" {
			span.SetStatus(codes.Error, outcome)
		}
		span.End()
		if histogram, err := meter().Float64Histogram("cliapiproxy.usage.poll.duration", metric.WithUnit("s"),
			metric.WithDescription("Duration of credential usage polls, by provider and outcome.")); err == nil {
			histogram.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		}
	}
}

// RemainingShare is a provider's lowest remaining share of one meter class.
type RemainingShare struct {
	Provider string
	Meter    string
	Share    float64
}

// RegisterRemainingGauge reports, when metrics are collected, each
// provider's lowest remaining share per meter class, and returns a function
// that unregisters it.
func RegisterRemainingGauge(read func() []RemainingShare) (func(), error) {
	m := meter()
	gauge, err := m.Float64ObservableGauge("cliapiproxy.usage.remaining", metric.WithUnit("1"),
		metric.WithDescription("Lowest remaining share of a usage meter across a provider's credentials."))
	if err != nil {
		return nil, err
	}
	registration, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, r := range read() {
			o.ObserveFloat64(gauge, r.Share, metric.WithAttributes(AttrProvider.String(r.Provider), AttrMeterClass.String(r.Meter)))
		}
		return nil
	}, gauge)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}
