// Package telemetry sets up OpenTelemetry for the server and holds the
// instrumentation the rest of the code uses. Setup belongs to the entry point;
// other packages use only the OpenTelemetry API through this package's helpers.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ScopeName is the instrumentation scope of every signal the server emits.
const ScopeName = "github.com/router-for-me/CLIProxyAPI/v8"

// Service identity reported as resource attributes.
const (
	ServiceNamespace = "cliapiproxy"
	ServiceName      = "cliapiproxy"
)

// Exporter selections.
const (
	ExporterOTLP    = "otlp"
	ExporterConsole = "console"
	ExporterNone    = "none"
)

// Bounds of batching and export.
const (
	exportTimeout  = 10 * time.Second
	maxQueueSize   = 2048
	batchInterval  = 5 * time.Second
	metricInterval = 60 * time.Second
)

// Settings select and address the exporters.
type Settings struct {
	// Traces, Metrics, and Logs each name an exporter selection.
	Traces, Metrics, Logs string
	// Endpoint is the OTLP/HTTP base URL; each signal appends its path.
	Endpoint string
	// Headers go with every OTLP request; they may carry credentials.
	Headers map[string]string
	// ServiceVersion is the build's immutable version.
	ServiceVersion string
	// Environment names the deployment environment.
	Environment string
	// Console receives the console exporters' output.
	Console io.Writer
}

// Providers are the SDK providers Setup installed.
type Providers struct {
	shutdown []func(context.Context) error
}

// Shutdown flushes and stops every provider within ctx.
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	for _, fn := range p.shutdown {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}

// ParseHeaders parses OTLP headers given as comma-separated key=value pairs
// with URL-encoded values. Errors never include the values.
func ParseHeaders(raw string) (map[string]string, error) {
	headers := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return headers, nil
	}
	for i, pair := range strings.Split(raw, ",") {
		key, encoded, found := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("header %d is not a key=value pair", i+1)
		}
		value, errUnescape := url.PathUnescape(strings.TrimSpace(encoded))
		if errUnescape != nil {
			return nil, fmt.Errorf("header %q has a malformed value", key)
		}
		headers[key] = value
	}
	return headers, nil
}

// Setup installs the global providers and the W3C Trace Context propagator.
// SDK errors, export failures included, are dropped: no fallback sink opens.
func Setup(ctx context.Context, s Settings) (*Providers, error) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	otel.SetLogger(logr.Discard())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	res := resource.NewSchemaless(
		attribute.String("service.namespace", ServiceNamespace),
		attribute.String("service.name", ServiceName),
		attribute.String("service.version", s.ServiceVersion),
		attribute.String("service.instance.id", uuid.NewString()),
		attribute.String("deployment.environment.name", s.Environment),
	)
	p := &Providers{}
	base := strings.TrimRight(s.Endpoint, "/")

	switch s.Traces {
	case ExporterOTLP, ExporterConsole:
		var exporter sdktrace.SpanExporter
		var errExporter error
		if s.Traces == ExporterOTLP {
			exporter, errExporter = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(base+"/v1/traces"),
				otlptracehttp.WithHeaders(s.Headers), otlptracehttp.WithTimeout(exportTimeout))
		} else {
			exporter, errExporter = stdouttrace.New(stdouttrace.WithWriter(s.Console))
		}
		if errExporter != nil {
			return nil, fmt.Errorf("trace exporter: %w", errExporter)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
			sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(maxQueueSize),
				sdktrace.WithBatchTimeout(batchInterval), sdktrace.WithExportTimeout(exportTimeout)),
		)
		otel.SetTracerProvider(tp)
		p.shutdown = append(p.shutdown, tp.Shutdown)
	case ExporterNone:
	default:
		return nil, fmt.Errorf("unknown trace exporter %q", s.Traces)
	}

	switch s.Metrics {
	case ExporterOTLP, ExporterConsole:
		var exporter sdkmetric.Exporter
		var errExporter error
		if s.Metrics == ExporterOTLP {
			exporter, errExporter = otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"),
				otlpmetrichttp.WithHeaders(s.Headers), otlpmetrichttp.WithTimeout(exportTimeout))
		} else {
			exporter, errExporter = stdoutmetric.New(stdoutmetric.WithWriter(s.Console))
		}
		if errExporter != nil {
			return nil, fmt.Errorf("metric exporter: %w", errExporter)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithInterval(metricInterval), sdkmetric.WithTimeout(exportTimeout))),
		)
		otel.SetMeterProvider(mp)
		p.shutdown = append(p.shutdown, mp.Shutdown)
	case ExporterNone:
	default:
		return nil, fmt.Errorf("unknown metric exporter %q", s.Metrics)
	}

	switch s.Logs {
	case ExporterOTLP, ExporterConsole:
		var exporter sdklog.Exporter
		var errExporter error
		if s.Logs == ExporterOTLP {
			exporter, errExporter = otlploghttp.New(ctx, otlploghttp.WithEndpointURL(base+"/v1/logs"),
				otlploghttp.WithHeaders(s.Headers), otlploghttp.WithTimeout(exportTimeout))
		} else {
			exporter, errExporter = stdoutlog.New(stdoutlog.WithWriter(s.Console))
		}
		if errExporter != nil {
			return nil, fmt.Errorf("log exporter: %w", errExporter)
		}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter, sdklog.WithMaxQueueSize(maxQueueSize),
				sdklog.WithExportInterval(batchInterval), sdklog.WithExportTimeout(exportTimeout))),
		)
		global.SetLoggerProvider(lp)
		p.shutdown = append(p.shutdown, lp.Shutdown)
	case ExporterNone:
	default:
		return nil, fmt.Errorf("unknown log exporter %q", s.Logs)
	}
	return p, nil
}
