package telemetry

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"slices"
)

//go:embed catalog.json
var catalogJSON []byte

// CatalogSchema is the producer signal catalog format catalog.json follows.
const CatalogSchema = "opentelemetry.producer/signal-catalog/v1"

// Signals a catalog lists, by their catalog keys.
const (
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
	SignalLogs    = "logs"
)

// Catalog is the versioned list of the signals the server emits, in the
// producer signal catalog format collectors derive their registrations from.
type Catalog struct {
	SchemaVersion    string   `json:"schema_version"`
	CatalogVersion   int      `json:"catalog_version"`
	ServiceNamespace string   `json:"service_namespace"`
	ServiceNames     []string `json:"service_names"`
	Catalog          struct {
		Traces                []string `json:"traces"`
		TraceNamePatterns     []string `json:"trace_name_patterns"`
		Metrics               []string `json:"metrics"`
		Logs                  []string `json:"logs"`
		CorrelationAttributes []string `json:"correlation_attributes"`
	} `json:"catalog"`
	AllowedAttributes map[string][]string `json:"allowed_attributes"`
	MetricDimensions  map[string][]string `json:"metric_dimensions"`
	MetricUnits       map[string]string   `json:"metric_units"`
	ErrorClasses      []string            `json:"error_classes"`
	// ErrorTypePatterns match the error.type values that are HTTP status codes.
	ErrorTypePatterns []string `json:"error_type_patterns"`
	Qualification     struct {
		TraceName            string `json:"trace_name"`
		MetricName           string `json:"metric_name"`
		LogName              string `json:"log_name"`
		CorrelationAttribute string `json:"correlation_attribute"`
	} `json:"qualification"`
}

// LoadCatalog parses the embedded signal catalog.
func LoadCatalog() (*Catalog, error) {
	var c Catalog
	if errUnmarshal := json.Unmarshal(catalogJSON, &c); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return &c, nil
}

// SpanListed reports whether the catalog lists a span name, by name or pattern.
func (c *Catalog) SpanListed(name string) bool {
	if slices.Contains(c.Catalog.Traces, name) {
		return true
	}
	for _, pattern := range c.Catalog.TraceNamePatterns {
		if matched, _ := regexp.MatchString(pattern, name); matched {
			return true
		}
	}
	return false
}

// AttributeAllowed reports whether a signal may carry the attribute.
func (c *Catalog) AttributeAllowed(signal, key string) bool {
	return slices.Contains(c.AllowedAttributes[signal], key)
}

// ValueAllowed reports whether a value of a bounded attribute is cataloged:
// a metric dimension's listed values, or an error class or status code.
func (c *Catalog) ValueAllowed(signal, key, value string) bool {
	if key == string(AttrErrorType) {
		if slices.Contains(c.ErrorClasses, value) {
			return true
		}
		for _, pattern := range c.ErrorTypePatterns {
			if matched, _ := regexp.MatchString(pattern, value); matched {
				return true
			}
		}
		return false
	}
	if values, bounded := c.MetricDimensions[key]; bounded && signal == SignalMetrics {
		return slices.Contains(values, value)
	}
	return true
}
