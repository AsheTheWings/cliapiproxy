package pruning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ContentType is the content type of a residual's body.
const ContentType = "application/vnd.cliapiproxy.transcript-ref+json"

// ResidualVersion is the residual format's version.
const ResidualVersion = 1

// A projection names where a request format keeps its item list.
type projection struct {
	name string
	path []string
}

// projections are tried in order; the first path that holds an array is the
// request's item list.
var projections = []projection{
	{"commandcode.messages", []string{"params", "messages"}},
	{"cloudcode.contents", []string{"request", "contents"}},
	{"responses.input", []string{"input"}},
	{"messages", []string{"messages"}},
	{"gemini.contents", []string{"contents"}},
}

func projectionNamed(name string) (projection, bool) {
	for _, p := range projections {
		if p.name == name {
			return p, true
		}
	}
	return projection{}, false
}

func (v *Value) member(key string) *Value {
	if v == nil || v.kind != kindObject {
		return nil
	}
	for _, m := range v.members {
		if m.Key == key {
			return m.Value
		}
	}
	return nil
}

func (v *Value) at(path []string) *Value {
	for _, key := range path {
		v = v.member(key)
	}
	return v
}

// with returns a copy of object v whose value at path is replaced, or
// removed when value is nil; objects on the path are created as needed.
func (v *Value) with(path []string, value *Value) *Value {
	out := &Value{kind: kindObject}
	if v != nil && v.kind == kindObject {
		out.members = slices.Clone(v.members)
	}
	key := path[0]
	i := slices.IndexFunc(out.members, func(m Member) bool { return m.Key == key })
	next := value
	if len(path) > 1 {
		var child *Value
		if i >= 0 {
			child = out.members[i].Value
		}
		next = child.with(path[1:], value)
	}
	switch {
	case next == nil && i >= 0:
		out.members = slices.Delete(out.members, i, i+1)
	case next == nil:
	case i >= 0:
		out.members[i].Value = next
	default:
		out.members = append(out.members, Member{Key: key, Value: next})
	}
	return out
}

// Transcript is a request split into its item list and the rest.
type Transcript struct {
	Projection string
	// Items are each item's canonical serialization.
	Items [][]byte
	items []*Value
	// Rest is the request without its item list.
	Rest *Value
	// SHA256 is the hex SHA-256 of the request's canonical serialization.
	SHA256 string
}

// ErrUnprojectable marks a request with no item list.
var ErrUnprojectable = errors.New("request has no item list")

// Project splits a canonicalizable request object into its item list and
// the rest.
func Project(body []byte) (*Transcript, error) {
	v, err := Parse(body)
	if err != nil {
		return nil, err
	}
	if v.kind != kindObject {
		return nil, ErrUnprojectable
	}
	for _, p := range projections {
		list := v.at(p.path)
		if list == nil || list.kind != kindArray {
			continue
		}
		t := &Transcript{Projection: p.name, items: list.items, Rest: v.with(p.path, nil), SHA256: digest(v.Canonical())}
		for _, item := range list.items {
			t.Items = append(t.Items, item.Canonical())
		}
		return t, nil
	}
	return nil, ErrUnprojectable
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Residual is a pruned input's stored body.
type Residual struct {
	Version    int    `json:"version"`
	Projection string `json:"projection"`
	// Items is how many leading items of the base the input shares.
	Items int `json:"items"`
	// SHA256 is the hex SHA-256 of the original request's RFC 8785
	// serialization.
	SHA256 string `json:"canonical_sha256"`
	// Rest is the request outside the item list in canonical form, or
	// absent when it equals the rest of the payload rest_source_payload_id
	// names.
	Rest json.RawMessage `json:"rest,omitempty"`
}

// ErrMalformedResidual marks a residual body the server cannot read.
var ErrMalformedResidual = errors.New("malformed residual")

// ParseResidual reads and validates a residual body.
func ParseResidual(body []byte) (Residual, error) {
	var r Residual
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Residual{}, fmt.Errorf("%w: %v", ErrMalformedResidual, err)
	}
	if _, ok := projectionNamed(r.Projection); !ok || r.Version != ResidualVersion || r.Items < 1 || len(r.SHA256) != 64 {
		return Residual{}, ErrMalformedResidual
	}
	if _, err := hex.DecodeString(r.SHA256); err != nil {
		return Residual{}, ErrMalformedResidual
	}
	if len(r.Rest) > 0 {
		rest, err := Parse(r.Rest)
		if err != nil || rest.kind != kindObject {
			return Residual{}, ErrMalformedResidual
		}
	}
	return r, nil
}

// ErrMismatch marks a rebuilt request whose digest differs from the
// residual's.
var ErrMismatch = errors.New("rebuilt request does not match its digest")

// Rebuild returns the canonical serialization of the request a residual
// stands for, from its base's full body and, when the residual omits its
// rest, the rest of its rest source as restSource. It checks the result
// against the residual's digest.
func Rebuild(residual Residual, base *Transcript, restSource *Value) ([]byte, error) {
	p, ok := projectionNamed(residual.Projection)
	if !ok || base == nil || base.Projection != residual.Projection || len(base.items) < residual.Items {
		return nil, fmt.Errorf("%w: base does not hold the residual's items", ErrMalformedResidual)
	}
	rest := restSource
	if len(residual.Rest) > 0 {
		parsed, err := Parse(residual.Rest)
		if err != nil {
			return nil, ErrMalformedResidual
		}
		rest = parsed
	}
	if rest == nil {
		return nil, fmt.Errorf("%w: no rest", ErrMalformedResidual)
	}
	list := &Value{kind: kindArray, items: base.items[:residual.Items]}
	out := rest.with(p.path, list).Canonical()
	if digest(out) != residual.SHA256 {
		return nil, ErrMismatch
	}
	return out, nil
}

// encode serializes a residual, leaving its canonical rest unescaped.
func (r Residual) encode() []byte {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(r)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
