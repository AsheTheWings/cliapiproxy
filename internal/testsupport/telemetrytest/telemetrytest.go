// Package telemetrytest records a test's spans, metrics, and log records in
// memory and checks them against the signal catalog.
package telemetrytest

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Recorder holds the in-memory signals of one test.
type Recorder struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	logs   *logRecorder
}

// Install routes the global providers to memory and the propagator to W3C
// Trace Context, restoring the previous globals when the test ends.
func Install(t testing.TB) *Recorder {
	t.Helper()
	r := &Recorder{spans: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader(), logs: &logRecorder{}}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r.spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.reader))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(r.logs))
	previousTracer, previousMeter, previousPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	previousLogger := global.GetLoggerProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	global.SetLoggerProvider(lp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousPropagator)
		global.SetLoggerProvider(previousLogger)
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
		_ = lp.Shutdown(context.Background())
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

// Logs returns the log records emitted so far.
func (r *Recorder) Logs() []sdklog.Record {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	return append([]sdklog.Record(nil), r.logs.records...)
}

// LogAttrs returns a log record's attributes.
func LogAttrs(record sdklog.Record) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	record.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs = append(attrs, kv)
		return true
	})
	return attrs
}

// CheckCatalog fails the test for any recorded span, metric, log record,
// attribute, unit, or bounded value the signal catalog does not list.
func (r *Recorder) CheckCatalog(t testing.TB) {
	t.Helper()
	catalog, errCatalog := telemetry.LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	checkAttrs := func(signal, name string, attrs []attribute.KeyValue) {
		for _, kv := range attrs {
			if !catalog.AttributeAllowed(signal, string(kv.Key)) {
				t.Errorf("%s %q carries attribute %s, which the catalog does not allow", signal, name, kv.Key)
			}
			if !catalog.ValueAllowed(signal, string(kv.Key), kv.Value.Emit()) {
				t.Errorf("%s %q carries %s=%q, which the catalog does not list", signal, name, kv.Key, kv.Value.Emit())
			}
		}
	}
	for _, s := range r.spans.Ended() {
		if !catalog.SpanListed(s.Name()) {
			t.Errorf("span %q is not in the catalog", s.Name())
		}
		checkAttrs(telemetry.SignalTraces, s.Name(), s.Attributes())
	}
	for _, m := range r.Metrics(t) {
		unit, listed := catalog.MetricUnits[m.Name]
		if !listed || !slices.Contains(catalog.Catalog.Metrics, m.Name) {
			t.Errorf("metric %q is not in the catalog", m.Name)
			continue
		}
		if m.Unit != unit {
			t.Errorf("metric %q has unit %q, want %q", m.Name, m.Unit, unit)
		}
		switch data := m.Data.(type) {
		case metricdata.Histogram[float64]:
			for _, dp := range data.DataPoints {
				checkAttrs(telemetry.SignalMetrics, m.Name, dp.Attributes.ToSlice())
			}
		case metricdata.Sum[int64]:
			for _, dp := range data.DataPoints {
				checkAttrs(telemetry.SignalMetrics, m.Name, dp.Attributes.ToSlice())
			}
		default:
			t.Errorf("metric %q has data of type %T, which CheckCatalog does not inspect", m.Name, m.Data)
		}
	}
	for _, record := range r.Logs() {
		body := record.Body().Emit()
		if !slices.Contains(catalog.Catalog.Logs, body) {
			t.Errorf("log record %q is not in the catalog", body)
		}
		checkAttrs(telemetry.SignalLogs, body, LogAttrs(record))
	}
}

// logRecorder is a log processor keeping every record it receives.
type logRecorder struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (l *logRecorder) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }

func (l *logRecorder) OnEmit(_ context.Context, record *sdklog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record.Clone())
	return nil
}

func (l *logRecorder) Shutdown(context.Context) error { return nil }

func (l *logRecorder) ForceFlush(context.Context) error { return nil }
