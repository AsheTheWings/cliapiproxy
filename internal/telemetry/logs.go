package telemetry

import (
	"context"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

// LogEventName is the body of every LogRecord a log statement produces.
const LogEventName = "cliapiproxy.log"

// Attribute keys of log events.
const (
	AttrCodeFilePath     = attribute.Key("code.file.path")
	AttrCodeLineNumber   = attribute.Key("code.line.number")
	AttrCodeFunctionName = attribute.Key("code.function.name")
)

// ExportedLogLevels are the logrus levels that produce log events.
var ExportedLogLevels = []logrus.Level{logrus.PanicLevel, logrus.FatalLevel, logrus.ErrorLevel, logrus.WarnLevel}

// sourceRoot is the path prefix of this module's files in caller frames.
var sourceRoot = func() string {
	_, file, _, _ := runtime.Caller(0)
	return strings.TrimSuffix(file, "internal/telemetry/logs.go")
}()

// modulePath prefixes this module's function names in caller frames.
const modulePath = "github.com/router-for-me/CLIProxyAPI/v8/"

// InstallLogEvents makes each warning or error the logger records emit one
// LogRecord through the global logger provider. A record identifies its log
// statement by source location and carries the error class of its "error"
// field; message text and other fields are never exported, since they are
// unbounded and can hold credentials or payloads. The caller stops the
// logger's own output.
func InstallLogEvents(logger *logrus.Logger) {
	logger.SetReportCaller(true)
	logger.AddHook(logEventHook{})
}

type logEventHook struct{}

func (logEventHook) Levels() []logrus.Level { return ExportedLogLevels }

func (logEventHook) Fire(entry *logrus.Entry) error {
	ctx := entry.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var record otellog.Record
	record.SetTimestamp(entry.Time)
	record.SetEventName(LogEventName)
	record.SetBody(attribute.StringValue(LogEventName))
	record.SetSeverity(severity(entry.Level))
	record.SetSeverityText(strings.ToUpper(entry.Level.String()))
	if entry.Caller != nil {
		record.AddAttributes(
			AttrCodeFilePath.String(strings.TrimPrefix(entry.Caller.File, sourceRoot)),
			AttrCodeLineNumber.Int(entry.Caller.Line),
			AttrCodeFunctionName.String(strings.TrimPrefix(entry.Caller.Function, modulePath)),
		)
	}
	if err, ok := entry.Data[logrus.ErrorKey].(error); ok {
		if class := ErrorClass(err); class != "" {
			record.AddAttributes(AttrErrorType.String(class))
		}
	}
	global.GetLoggerProvider().Logger(ScopeName).Emit(ctx, record)
	return nil
}

func severity(level logrus.Level) otellog.Severity {
	switch level {
	case logrus.PanicLevel, logrus.FatalLevel:
		return otellog.SeverityFatal
	case logrus.ErrorLevel:
		return otellog.SeverityError
	default:
		return otellog.SeverityWarn
	}
}
