package telemetry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Attribute keys of the signal catalog.
const (
	AttrHTTPMethod     = attribute.Key("http.request.method")
	AttrHTTPRoute      = attribute.Key("http.route")
	AttrHTTPStatusCode = attribute.Key("http.response.status_code")
	AttrErrorType      = attribute.Key("error.type")
	AttrOperation      = attribute.Key("cliapiproxy.operation")
	AttrProvider       = attribute.Key("cliapiproxy.provider")
)

// Error classes, the stable values of error.type for failures outside HTTP status codes.
const (
	ErrorCanceled      = "canceled"
	ErrorTimeout       = "timeout"
	ErrorUnauthorized  = "unauthorized"
	ErrorRateLimited   = "rate_limited"
	ErrorClient        = "client_error"
	ErrorUpstream      = "upstream_error"
	ErrorNoCredential  = "no_credential"
	ErrorNetwork       = "network"
	ErrorOther         = "other"
	multipleProviders  = "multiple"
	httpMethodOther    = "_OTHER"
	serverSpanFallback = "HTTP"
)

func tracer() trace.Tracer { return otel.Tracer(ScopeName) }

func meter() metric.Meter { return otel.Meter(ScopeName) }

var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	http.MethodDelete: true, http.MethodConnect: true, http.MethodOptions: true, http.MethodTrace: true,
}

// GinMiddleware traces each request as a server span named for its route
// template, continuing the caller's W3C trace context, and records its duration.
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		method := c.Request.Method
		if !knownMethods[method] {
			method = httpMethodOther
		}
		route := c.FullPath()
		name := serverSpanFallback
		if method != httpMethodOther {
			name = method
		}
		attrs := []attribute.KeyValue{AttrHTTPMethod.String(method)}
		if route != "" {
			name += " " + route
			attrs = append(attrs, AttrHTTPRoute.String(route))
		}
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, span := tracer().Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
		c.Request = c.Request.WithContext(ctx)
		defer span.End()

		c.Next()

		status := c.Writer.Status()
		attrs = append(attrs, AttrHTTPStatusCode.Int(status))
		span.SetAttributes(AttrHTTPStatusCode.Int(status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "")
			span.SetAttributes(AttrErrorType.String(strconv.Itoa(status)))
			attrs = append(attrs, AttrErrorType.String(strconv.Itoa(status)))
		}
		if histogram, errHistogram := meter().Float64Histogram("http.server.request.duration",
			metric.WithUnit("s"), metric.WithDescription("Duration of HTTP server requests.")); errHistogram == nil {
			histogram.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		}
	}
}

// ErrorClass maps a failure to its stable error class, or "" for success.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorTimeout
	}
	var withStatus interface{ StatusCode() int }
	if errors.As(err, &withStatus) {
		if class := statusClass(withStatus.StatusCode()); class != "" {
			return class
		}
	}
	var withCode interface{ ErrorCode() string }
	if errors.As(err, &withCode) {
		switch withCode.ErrorCode() {
		case "auth_not_found", "provider_not_found", "executor_not_found", "auth_unavailable":
			return ErrorNoCredential
		case "rate_limit", "rate_limit_exceeded", "rate_limited", "quota_exhausted", "usage_limit_reached":
			return ErrorRateLimited
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ErrorTimeout
		}
		return ErrorNetwork
	}
	return ErrorOther
}

func statusClass(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrorUnauthorized
	case status == http.StatusTooManyRequests:
		return ErrorRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return ErrorTimeout
	case status >= 400 && status < 500:
		return ErrorClient
	case status >= 500:
		return ErrorUpstream
	}
	return ""
}

// StartExecution starts the span of one model execution across its credential
// attempts; finish ends it with the execution's outcome and records its duration.
func StartExecution(ctx context.Context, operation string, providers []string) (_ context.Context, finish func(error)) {
	start := time.Now()
	provider := multipleProviders
	if len(providers) == 1 {
		provider = providers[0]
	}
	attrs := []attribute.KeyValue{AttrOperation.String(operation), AttrProvider.String(provider)}
	ctx, span := tracer().Start(ctx, "cliapiproxy.execute", trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if class := ErrorClass(err); class != "" {
			span.SetStatus(codes.Error, class)
			span.SetAttributes(AttrErrorType.String(class))
			attrs = append(attrs, AttrErrorType.String(class))
		}
		span.End()
		if histogram, errHistogram := meter().Float64Histogram("cliapiproxy.execute.duration",
			metric.WithUnit("s"), metric.WithDescription("Duration of model executions across credential attempts.")); errHistogram == nil {
			histogram.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		}
	}
}
