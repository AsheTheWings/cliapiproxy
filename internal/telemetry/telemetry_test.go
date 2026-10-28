//go:build unit

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

type statusError int

func (e statusError) Error() string   { return "status" }
func (e statusError) StatusCode() int { return int(e) }

type codeError string

func (e codeError) Error() string     { return string(e) }
func (e codeError) ErrorCode() string { return string(e) }

func TestErrorClassIsStableAndCataloged(t *testing.T) {
	catalog, errCatalog := LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	for err, want := range map[error]string{
		context.Canceled: ErrorCanceled,
		fmt.Errorf("wrap: %w", context.DeadlineExceeded): ErrorTimeout,
		statusError(http.StatusUnauthorized):             ErrorUnauthorized,
		statusError(http.StatusTooManyRequests):          ErrorRateLimited,
		statusError(http.StatusBadRequest):               ErrorClient,
		statusError(http.StatusBadGateway):               ErrorUpstream,
		codeError("auth_not_found"):                      ErrorNoCredential,
		codeError("quota_exhausted"):                     ErrorRateLimited,
		errors.New("boom"):                               ErrorOther,
	} {
		got := ErrorClass(err)
		if got != want {
			t.Errorf("ErrorClass(%v) = %q, want %q", err, got, want)
		}
		if !catalog.ValueAllowed(SignalTraces, string(AttrErrorType), got) {
			t.Errorf("error class %q is not in the catalog", got)
		}
	}
	if ErrorClass(nil) != "" {
		t.Fatal("success has no error class")
	}
}

func TestParseHeadersKeepsValuesOutOfErrors(t *testing.T) {
	headers, err := ParseHeaders("authorization=Basic%20dXNlcjpwYXNz, x-scope = a%2Cb")
	if err != nil || headers["authorization"] != "Basic dXNlcjpwYXNz" || headers["x-scope"] != "a,b" {
		t.Fatalf("headers = %v, err = %v", headers, err)
	}
	for _, raw := range []string{"authorization", "=v", "k=%zz-secret"} {
		_, err := ParseHeaders(raw)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("ParseHeaders(%q) error = %v", raw, err)
		}
	}
}

func TestDebugAndInfoRecordsProduceNoLogEvents(t *testing.T) {
	for _, level := range []logrus.Level{logrus.InfoLevel, logrus.DebugLevel, logrus.TraceLevel} {
		if slices.Contains(ExportedLogLevels, level) {
			t.Fatalf("%s records must not produce log events", level)
		}
	}
}

// Collectors derive their registrations from the catalog, so it must satisfy
// the producer signal catalog format and agree with the instrumentation.
func TestCatalogFollowsTheProducerCatalogFormat(t *testing.T) {
	catalog, errCatalog := LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	if catalog.SchemaVersion != CatalogSchema || catalog.CatalogVersion < 1 {
		t.Fatalf("schema %q, version %d", catalog.SchemaVersion, catalog.CatalogVersion)
	}
	if catalog.ServiceNamespace != ServiceNamespace || !slices.Equal(catalog.ServiceNames, []string{ServiceName}) {
		t.Fatalf("identity %q %v differs from the resource", catalog.ServiceNamespace, catalog.ServiceNames)
	}
	name := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]{0,254}[a-z0-9])?$`)
	for _, names := range [][]string{catalog.Catalog.Traces, catalog.Catalog.Metrics, catalog.Catalog.Logs} {
		for _, n := range names {
			if !name.MatchString(n) {
				t.Errorf("signal name %q is not a catalog name", n)
			}
		}
	}
	for _, pattern := range catalog.Catalog.TraceNamePatterns {
		if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") || len(pattern) > 256 {
			t.Errorf("trace name pattern %q must be anchored and at most 256 characters", pattern)
		}
	}
	for _, server := range []string{"GET /v1/models", "POST /v1/chat/completions", "GET", "HTTP /v1/models/:id"} {
		if !catalog.SpanListed(server) {
			t.Errorf("server span %q is not listed", server)
		}
	}
	q := catalog.Qualification
	if q.TraceName != QualificationName || q.MetricName != QualificationName || q.LogName != QualificationName ||
		q.CorrelationAttribute != string(AttrCorrelationID) {
		t.Fatalf("qualification %+v differs from the probe", q)
	}
	if !slices.Contains(catalog.Catalog.Traces, q.TraceName) || !slices.Contains(catalog.Catalog.Metrics, q.MetricName) ||
		!slices.Contains(catalog.Catalog.Logs, q.LogName) {
		t.Fatal("qualification names must be cataloged")
	}
	if !slices.Contains(catalog.Catalog.CorrelationAttributes, q.CorrelationAttribute) ||
		!catalog.AttributeAllowed(SignalTraces, q.CorrelationAttribute) || !catalog.AttributeAllowed(SignalLogs, q.CorrelationAttribute) ||
		catalog.AttributeAllowed(SignalMetrics, q.CorrelationAttribute) {
		t.Fatal("the correlation attribute must be allowed on traces and logs and never on metrics")
	}
	for key := range catalog.MetricDimensions {
		if !catalog.AttributeAllowed(SignalMetrics, key) {
			t.Errorf("metric dimension %s is not an allowed metric attribute", key)
		}
	}
	for _, metricName := range catalog.Catalog.Metrics {
		if _, ok := catalog.MetricUnits[metricName]; !ok {
			t.Errorf("metric %s has no unit", metricName)
		}
	}
	for _, class := range []string{ErrorCanceled, ErrorTimeout, ErrorUnauthorized, ErrorRateLimited, ErrorClient,
		ErrorUpstream, ErrorNoCredential, ErrorNetwork, ErrorOther} {
		if !slices.Contains(catalog.ErrorClasses, class) {
			t.Errorf("error class %s is not cataloged", class)
		}
	}
}
