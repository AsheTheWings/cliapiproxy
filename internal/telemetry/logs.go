package telemetry

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/bridges/otellogrus"
	"go.opentelemetry.io/otel/log"
)

// ExportedLogLevels are the logrus levels exported as LogRecords. Debug and
// trace records can carry payloads, so they are never exported.
var ExportedLogLevels = []logrus.Level{logrus.PanicLevel, logrus.FatalLevel, logrus.ErrorLevel, logrus.WarnLevel, logrus.InfoLevel}

// InstallLogBridge makes logger emit its records as OpenTelemetry LogRecords,
// scrubbed of credentials. The caller stops the logger's own output.
func InstallLogBridge(logger *logrus.Logger, provider log.LoggerProvider, version string) {
	logger.AddHook(scrubbingHook{inner: otellogrus.NewHook(ScopeName,
		otellogrus.WithLoggerProvider(provider),
		otellogrus.WithLevels(ExportedLogLevels),
		otellogrus.WithVersion(version),
	)})
}

// scrubbingHook removes credentials from a record before passing it on.
type scrubbingHook struct {
	inner logrus.Hook
}

func (h scrubbingHook) Levels() []logrus.Level { return h.inner.Levels() }

func (h scrubbingHook) Fire(entry *logrus.Entry) error {
	scrubbed := *entry
	scrubbed.Message = Scrub(entry.Message)
	scrubbed.Data = make(logrus.Fields, len(entry.Data))
	for key, value := range entry.Data {
		scrubbed.Data[key] = scrubValue(value)
	}
	return h.inner.Fire(&scrubbed)
}

func scrubValue(value any) any {
	switch v := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case string:
		return Scrub(v)
	case error:
		return errors.New(Scrub(v.Error()))
	default:
		return Scrub(fmt.Sprint(v))
	}
}

var scrubPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	// Authorization schemes followed by a credential.
	{regexp.MustCompile(`(?i)\b(bearer|basic|token)(\s+)[A-Za-z0-9._~+/=:-]{8,}`), "$1$2[redacted]"},
	// JSON Web Tokens.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`), "[redacted]"},
	// Provider API key formats.
	{regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_-]{16,}|\bAIza[0-9A-Za-z_-]{30,}|\bgh[pousr]_[A-Za-z0-9]{20,}|\bxai-[A-Za-z0-9]{20,}`), "[redacted]"},
	// Credentials in a URL's user information.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "$1[redacted]@"},
	// Secret query parameters.
	{regexp.MustCompile(`(?i)([?&](?:key|api_key|apikey|token|access_token|refresh_token|id_token|code|client_secret|password|secret|signature|sig)=)[^&\s"']+`), "$1[redacted]"},
	// Secret fields in JSON or key=value text.
	{regexp.MustCompile(`(?i)("?(?:access_token|refresh_token|id_token|api_key|apikey|client_secret|password|secret|authorization|cookie)"?\s*[:=]\s*"?)[^"\s,}&]+`), "$1[redacted]"},
}

// Scrub replaces credentials in s: authorization values, tokens, provider
// API keys, URL user information, and secret query parameters or fields.
func Scrub(s string) string {
	for _, p := range scrubPatterns {
		s = p.pattern.ReplaceAllString(s, p.replacement)
	}
	return s
}
