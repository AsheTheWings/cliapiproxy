//go:build component

package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func TestServerSpanContinuesTheCallersTraceUnderItsRoute(t *testing.T) {
	rec := telemetrytest.Install(t)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(telemetry.GinMiddleware())
	engine.GET("/v1/models/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.POST("/v1/fail", func(c *gin.Context) { c.Status(http.StatusBadGateway) })

	req := httptest.NewRequest(http.MethodGet, "/v1/models/gpt-x?key=secret", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	engine.ServeHTTP(httptest.NewRecorder(), req)
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/fail", nil))
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/nowhere", nil))

	span := rec.Span(t, "GET /v1/models/:id")
	if span.SpanKind() != trace.SpanKindServer || span.Parent().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("span kind %v, parent %v; want a server span continuing the caller's trace", span.SpanKind(), span.Parent())
	}
	if route, _ := telemetrytest.Attr(span, telemetry.AttrHTTPRoute); route.AsString() != "/v1/models/:id" {
		t.Fatalf("http.route = %q, want the template", route.AsString())
	}
	failed := rec.Span(t, "POST /v1/fail")
	if errType, _ := telemetrytest.Attr(failed, telemetry.AttrErrorType); errType.AsString() != "502" {
		t.Fatalf("error.type = %q, want 502", errType.AsString())
	}
	rec.Span(t, "HTTP")
	rec.CheckCatalog(t)
}

func TestLogStatementsBecomeBoundedEvents(t *testing.T) {
	rec := telemetrytest.Install(t)
	logger := logrus.New()
	logger.SetOutput(&strings.Builder{})
	telemetry.InstallLogEvents(logger)

	logger.WithError(context.DeadlineExceeded).WithField("api_key", "sk-secret-value").Error("refresh with Bearer abc123 failed")
	logger.Warn("plain warning")
	logger.Info("routine")
	logger.Debug("payload")

	logs := rec.Logs()
	if len(logs) != 2 {
		t.Fatalf("%d log records, want one per warning or error", len(logs))
	}
	first := logs[0]
	if first.Body().Emit() != telemetry.LogEventName || first.Severity() != otellog.SeverityError {
		t.Fatalf("body %q severity %v", first.Body().Emit(), first.Severity())
	}
	attrs := attrsOf(first)
	if attrs[string(telemetry.AttrCodeFilePath)] != "internal/telemetry/signals_test.go" ||
		attrs[string(telemetry.AttrCodeFunctionName)] != "internal/telemetry_test.TestLogStatementsBecomeBoundedEvents" ||
		attrs[string(telemetry.AttrCodeLineNumber)] == "" || attrs[string(telemetry.AttrErrorType)] != telemetry.ErrorTimeout {
		t.Fatalf("attributes = %v, want the statement's location and error class", attrs)
	}
	for _, record := range logs {
		for _, kv := range telemetrytest.LogAttrs(record) {
			if strings.Contains(kv.Value.Emit(), "secret") || strings.Contains(kv.Value.Emit(), "abc123") {
				t.Fatalf("log record exports %s=%q", kv.Key, kv.Value.Emit())
			}
		}
	}
	if _, hasClass := attrsOf(logs[1])[string(telemetry.AttrErrorType)]; hasClass {
		t.Fatal("a record without an error carries an error class")
	}
	rec.CheckCatalog(t)
}

func TestProbeEmitsCorrelatedQualificationSignals(t *testing.T) {
	rec := telemetrytest.Install(t)
	id := telemetry.Probe(context.Background())
	if id == "" {
		t.Fatal("the probe returned no correlation identifier")
	}
	span := rec.Span(t, telemetry.QualificationName)
	if v, _ := telemetrytest.Attr(span, telemetry.AttrCorrelationID); v.AsString() != id {
		t.Fatalf("span correlation %q, want %q", v.AsString(), id)
	}
	logs := rec.Logs()
	if len(logs) != 1 || logs[0].Body().Emit() != telemetry.QualificationName ||
		attrsOf(logs[0])[string(telemetry.AttrCorrelationID)] != id || logs[0].TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("logs = %d, want one qualification record in the probe's trace with its correlation", len(logs))
	}
	counted := false
	for _, m := range rec.Metrics(t) {
		counted = counted || m.Name == telemetry.QualificationName
	}
	if !counted {
		t.Fatal("the probe recorded no qualification metric")
	}
	rec.CheckCatalog(t)
}

func attrsOf(record sdklog.Record) map[string]string {
	attrs := map[string]string{}
	for _, kv := range telemetrytest.LogAttrs(record) {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	return attrs
}

func TestExecutionSpanRecordsItsErrorClass(t *testing.T) {
	rec := telemetrytest.Install(t)
	_, finish := telemetry.StartExecution(context.Background(), "stream", []string{"codex"})
	finish(context.DeadlineExceeded)
	_, finish = telemetry.StartExecution(context.Background(), "execute", []string{"codex", "xai"})
	finish(nil)

	spans := rec.Spans("cliapiproxy.execute")
	if len(spans) != 2 {
		t.Fatalf("%d execution spans, want 2", len(spans))
	}
	if errType, _ := telemetrytest.Attr(spans[0], telemetry.AttrErrorType); errType.AsString() != telemetry.ErrorTimeout {
		t.Fatalf("error.type = %q, want timeout", errType.AsString())
	}
	if provider, _ := telemetrytest.Attr(spans[1], telemetry.AttrProvider); provider.AsString() != "multiple" {
		t.Fatalf("provider = %q, want multiple", provider.AsString())
	}
	if _, hasError := telemetrytest.Attr(spans[1], telemetry.AttrErrorType); hasError {
		t.Fatal("a successful execution carries an error class")
	}
	rec.CheckCatalog(t)
}
