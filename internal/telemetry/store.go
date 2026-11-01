package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Attribute keys of the generations store's signals.
const (
	AttrTokenType    = attribute.Key("cliapiproxy.token.type")
	AttrDropReason   = attribute.Key("cliapiproxy.store.drop_reason")
	AttrWriteOutcome = attribute.Key("cliapiproxy.store.write_outcome")
)

// Reasons a generation is dropped before the store keeps it.
const (
	DropQueueEntries  = "queue_entries"
	DropQueueBytes    = "queue_bytes"
	DropTerminalWrite = "terminal_write"
	DropShutdown      = "shutdown"
)

// Outcomes of one write attempt.
const (
	WriteOK        = "ok"
	WriteRetryable = "retryable"
	WriteTerminal  = "terminal"
)

// TokenCount is one token type's count of a stored generation.
type TokenCount struct {
	Type  string
	Count int64
}

// RecordGenerationTokens counts a stored generation's tokens by provider and
// token type.
func RecordGenerationTokens(ctx context.Context, provider string, counts []TokenCount) {
	counter, err := meter().Int64Counter("cliapiproxy.generation.tokens", metric.WithUnit("{token}"),
		metric.WithDescription("Tokens of stored generations, by provider and token type."))
	if err != nil {
		return
	}
	for _, c := range counts {
		if c.Count > 0 {
			counter.Add(ctx, c.Count, metric.WithAttributes(AttrProvider.String(provider), AttrTokenType.String(c.Type)))
		}
	}
}

// RecordGenerationDropped counts a generation the store did not keep.
func RecordGenerationDropped(ctx context.Context, reason string, n int64) {
	counter, err := meter().Int64Counter("cliapiproxy.store.dropped", metric.WithUnit("{generation}"),
		metric.WithDescription("Generations dropped before the store kept them, by reason."))
	if err == nil && n > 0 {
		counter.Add(ctx, n, metric.WithAttributes(AttrDropReason.String(reason)))
	}
}

// RecordStoreWrite records the duration of one write attempt by outcome.
func RecordStoreWrite(ctx context.Context, outcome string, d time.Duration) {
	histogram, err := meter().Float64Histogram("cliapiproxy.store.write.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of writing one generation to the store, by outcome."))
	if err == nil {
		histogram.Record(ctx, d.Seconds(), metric.WithAttributes(AttrWriteOutcome.String(outcome)))
	}
}

// StoreGauges reports the write queue and the database size when metrics
// are collected; each function returns its current value.
type StoreGauges struct {
	QueueEntries func() int64
	QueueBytes   func() int64
	// DatabaseSize returns the last measured size and whether one exists.
	DatabaseSize func() (int64, bool)
}

// RegisterStoreGauges registers the store's gauges and returns a function
// that unregisters them.
func RegisterStoreGauges(g StoreGauges) (func(), error) {
	m := meter()
	entries, err := m.Int64ObservableGauge("cliapiproxy.store.queue.entries", metric.WithUnit("{generation}"),
		metric.WithDescription("Generations waiting in the store's write queue."))
	if err != nil {
		return nil, err
	}
	bytesGauge, err := m.Int64ObservableGauge("cliapiproxy.store.queue.bytes", metric.WithUnit("By"),
		metric.WithDescription("Compressed payload bytes waiting in the store's write queue."))
	if err != nil {
		return nil, err
	}
	size, err := m.Int64ObservableGauge("cliapiproxy.store.database.size", metric.WithUnit("By"),
		metric.WithDescription("Size of the store's database."))
	if err != nil {
		return nil, err
	}
	registration, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(entries, g.QueueEntries())
		o.ObserveInt64(bytesGauge, g.QueueBytes())
		if n, ok := g.DatabaseSize(); ok {
			o.ObserveInt64(size, n)
		}
		return nil
	}, entries, bytesGauge, size)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}

// RegisterDumpAgeGauge reports the age in seconds of the store's newest
// promoted dump, which age returns with whether one exists, and returns a
// function that unregisters it.
func RegisterDumpAgeGauge(age func() (float64, bool)) (func(), error) {
	m := meter()
	gauge, err := m.Float64ObservableGauge("cliapiproxy.store.dump.age", metric.WithUnit("s"),
		metric.WithDescription("Age of the store's newest promoted dump."))
	if err != nil {
		return nil, err
	}
	registration, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if seconds, ok := age(); ok {
			o.ObserveFloat64(gauge, seconds)
		}
		return nil
	}, gauge)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}
