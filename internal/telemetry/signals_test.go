//go:build component

package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/telemetry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/testsupport/telemetrytest"
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
