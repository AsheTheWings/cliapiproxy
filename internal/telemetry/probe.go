package telemetry

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
)

// QualificationName names the probe's span, metric, and log record.
const QualificationName = "cliapiproxy.qualification"

// AttrCorrelationID ties the probe's span and log record together.
const AttrCorrelationID = attribute.Key("cliapiproxy.correlation.id")

// Probe emits one qualification span, metric point, and log record and
// returns the correlation identifier the span and the record carry. It
// proves the signals left the process only once the providers flushed.
func Probe(ctx context.Context) string {
	id := uuid.NewString()
	ctx, span := tracer().Start(ctx, QualificationName)
	span.SetAttributes(AttrCorrelationID.String(id))
	if counter, errCounter := meter().Int64Counter(QualificationName, metric.WithUnit("{probe}"),
		metric.WithDescription("Telemetry probes run.")); errCounter == nil {
		counter.Add(ctx, 1)
	}
	var record otellog.Record
	record.SetTimestamp(time.Now())
	record.SetEventName(QualificationName)
	record.SetBody(attribute.StringValue(QualificationName))
	record.SetSeverity(otellog.SeverityInfo)
	record.SetSeverityText("INFO")
	record.AddAttributes(AttrCorrelationID.String(id))
	global.GetLoggerProvider().Logger(ScopeName).Emit(ctx, record)
	span.End()
	return id
}
