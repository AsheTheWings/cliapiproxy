package telemetry

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"slices"
)

//go:embed catalog.json
var catalogJSON []byte

// Catalog is the versioned list of the signals the server emits.
type Catalog struct {
	Version int `json:"version"`
	Spans   []struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Attributes []string `json:"attributes"`
	} `json:"spans"`
	Metrics []struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Unit       string   `json:"unit"`
		Attributes []string `json:"attributes"`
	} `json:"metrics"`
	AttributeValues map[string][]string `json:"attributeValues"`
}

// LoadCatalog parses the embedded signal catalog.
func LoadCatalog() (*Catalog, error) {
	var c Catalog
	if errUnmarshal := json.Unmarshal(catalogJSON, &c); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return &c, nil
}

var (
	serverSpanName = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE|CONNECT|OPTIONS|TRACE|HTTP)( /\S*)?$`)
	serverError    = regexp.MustCompile(`^5[0-9][0-9]$`)
)

// SpanAttributes returns the attributes a span of the name may carry, and
// whether the catalog lists the span.
func (c *Catalog) SpanAttributes(name string) ([]string, bool) {
	for _, s := range c.Spans {
		if s.Name == name || (s.Kind == "server" && serverSpanName.MatchString(name)) {
			return s.Attributes, true
		}
	}
	return nil, false
}

// MetricAttributes returns the dimensions and unit of a metric, and whether the catalog lists it.
func (c *Catalog) MetricAttributes(name string) ([]string, string, bool) {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m.Attributes, m.Unit, true
		}
	}
	return nil, "", false
}

// ValueAllowed reports whether value is a cataloged value of a bounded attribute.
func (c *Catalog) ValueAllowed(key, value string) bool {
	allowed, bounded := c.AttributeValues[key]
	if !bounded {
		return true
	}
	return slices.Contains(allowed, value) || (key == string(AttrErrorType) && slices.Contains(allowed, "5xx") && serverError.MatchString(value))
}
