// Package telemetrytest records a test's spans and metrics in memory and
// checks them against the signal catalog.
package telemetrytest

import (
	"context"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Recorder holds the in-memory signals of one test.
type Recorder struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// Install routes the global providers to memory and the propagator to W3C
// Trace Context, restoring the previous globals when the test ends.
func Install(t testing.TB) *Recorder {
	t.Helper()
	r := &Recorder{spans: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r.spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.reader))
	previousTracer, previousMeter, previousPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousPropagator)
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return r
}

// Spans returns the ended spans with the name.
func (r *Recorder) Spans(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range r.spans.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

// Span returns the only ended span with the name.
func (r *Recorder) Span(t testing.TB, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := r.Spans(name)
	if len(spans) != 1 {
		t.Fatalf("%d spans named %q, want 1", len(spans), name)
	}
	return spans[0]
}

// Attr returns a span attribute's value.
func Attr(span sdktrace.ReadOnlySpan, key attribute.Key) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// Metrics collects the metrics recorded so far.
func (r *Recorder) Metrics(t testing.TB) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if errCollect := r.reader.Collect(context.Background(), &rm); errCollect != nil {
		t.Fatal(errCollect)
	}
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

// CheckCatalog fails the test for any recorded span, metric, attribute, or
// bounded attribute value the signal catalog does not list.
func (r *Recorder) CheckCatalog(t testing.TB) {
	t.Helper()
	catalog, errCatalog := telemetry.LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	checkAttrs := func(signal string, allowed []string, attrs []attribute.KeyValue) {
		for _, kv := range attrs {
			if !slices.Contains(allowed, string(kv.Key)) {
				t.Errorf("%s carries attribute %s, which the catalog does not list", signal, kv.Key)
			}
			if !catalog.ValueAllowed(string(kv.Key), kv.Value.Emit()) {
				t.Errorf("%s carries %s=%q, which the catalog does not list", signal, kv.Key, kv.Value.Emit())
			}
		}
	}
	for _, s := range r.spans.Ended() {
		allowed, listed := catalog.SpanAttributes(s.Name())
		if !listed {
			t.Errorf("span %q is not in the catalog", s.Name())
			continue
		}
		checkAttrs("span "+s.Name(), allowed, s.Attributes())
	}
	for _, m := range r.Metrics(t) {
		allowed, unit, listed := catalog.MetricAttributes(m.Name)
		if !listed {
			t.Errorf("metric %q is not in the catalog", m.Name)
			continue
		}
		if m.Unit != unit {
			t.Errorf("metric %q has unit %q, want %q", m.Name, m.Unit, unit)
		}
		if histogram, ok := m.Data.(metricdata.Histogram[float64]); ok {
			for _, dp := range histogram.DataPoints {
				checkAttrs("metric "+m.Name, allowed, dp.Attributes.ToSlice())
			}
		}
	}
}
