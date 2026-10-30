//go:build unit

package keystate

import (
	"strings"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"gopkg.in/yaml.v3"
)

func TestParseRoundTripsItsEncoding(t *testing.T) {
	state := State{
		ClientKeys:     []string{"client-1", "client-2"},
		ClientKeyNames: []string{"Alice's laptop", ""},
		Upstream: map[string][]upstream.Group{
			upstream.FamilyOpenAICompatibility: {{Spec: upstream.Spec{Name: "alpha", BaseURL: "https://alpha.invalid/v1", Models: []upstream.Model{{Name: "m"}}}, Keys: []upstream.Key{{APIKey: "k1"}, {APIKey: "k2"}}}},
			upstream.FamilyKiro:                {{Spec: upstream.Spec{Name: "kiro", Prefix: "k"}, Keys: []upstream.Key{{APIKey: "k3"}}}},
		},
	}
	data, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := parsed.Encode()
	if string(again) != string(data) {
		t.Fatalf("round trip changed the state:\n%s\nwant\n%s", again, data)
	}
	if empty, err := Parse([]byte("\n")); err != nil || len(empty.ClientKeys)+len(empty.Upstream) != 0 {
		t.Fatalf("empty file = %v, %v; want the empty state", empty, err)
	}
}

func TestParseRejectsBrokenKeysWithoutNamingThem(t *testing.T) {
	for name, data := range map[string]string{
		"unknown family":      "api-keys: {gemini: [{name: g, keys: [{api-key: secret-x}]}]}",
		"unknown field":       "api-keys: {kiro: [{name: k, region: eu, keys: [{api-key: secret-x}]}]}",
		"unknown section":     "routing: {strategy: fill-first}",
		"no key":              "api-keys: {kiro: [{name: k, keys: []}]}",
		"empty key":           "api-keys: {kiro: [{name: k, keys: [{api-key: ''}]}]}",
		"repeated key":        "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}, {api-key: secret-x}]}]}",
		"base-url on kiro":    "api-keys: {kiro: [{name: k, base-url: 'https://k.invalid', keys: [{api-key: secret-x}]}]}",
		"no models":           "api-keys: {openai-compatibility: [{name: a, base-url: 'https://a.invalid', keys: [{api-key: secret-x}]}]}",
		"twice":               "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}]}, {name: k, keys: [{api-key: secret-y}]}]}",
		"empty client key":    "access: {api-keys: ['']}",
		"repeated client key": "access: {api-keys: [secret-c, secret-c]}",
		"names miscounted":    "access: {api-keys: [secret-c], api-key-names: [a, b]}",
		"name too long":       "access: {api-keys: [secret-c], api-key-names: [" + strings.Repeat("n", MaxClientKeyNameLength+1) + "]}",
		"name with a newline": "access: {api-keys: [secret-c], api-key-names: [\"a\\nb\"]}",
		"name padded":         "access: {api-keys: [secret-c], api-key-names: [' a']}",
	} {
		_, err := Parse([]byte(data))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Errorf("%s: error names a key: %v", name, err)
		}
	}
}

func TestMergeAddsTheStateAsTheOnlySourceOfKeys(t *testing.T) {
	document := []byte("config-version: 8\nrouting: {strategy: fill-first}\n")
	state := State{
		ClientKeys:     []string{"client-1"},
		ClientKeyNames: []string{"ci runner"},
		Upstream:       map[string][]upstream.Group{upstream.FamilyKiro: {{Spec: upstream.Spec{Name: "k"}, Keys: []upstream.Key{{APIKey: "k1"}}}}},
	}
	merged, err := Merge(document, state)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Version int `yaml:"config-version"`
		Routing struct {
			Strategy string `yaml:"strategy"`
		} `yaml:"routing"`
		Access struct {
			APIKeys []string `yaml:"api-keys"`
		} `yaml:"access"`
		APIKeys map[string][]upstream.Group `yaml:"api-keys"`
	}
	if err = yaml.Unmarshal(merged, &root); err != nil {
		t.Fatal(err)
	}
	if root.Version != 8 || root.Routing.Strategy != "fill-first" || len(root.Access.APIKeys) != 1 || root.Access.APIKeys[0] != "client-1" || root.APIKeys["kiro"][0].Keys[0].APIKey != "k1" {
		t.Fatalf("merged document = %s", merged)
	}
	if strings.Contains(string(merged), "ci runner") {
		t.Fatalf("merged document holds a key name: %s", merged)
	}
	for _, declared := range []string{"api-keys: {kiro: []}\n", "access: {api-keys: [k]}\n"} {
		if _, err = Merge([]byte(declared), state); err == nil {
			t.Errorf("a document declaring %q merged", declared)
		}
	}
}

func TestWithReplacesOnePart(t *testing.T) {
	state := State{ClientKeys: []string{"c"}, Upstream: map[string][]upstream.Group{upstream.FamilyKiro: {{Spec: upstream.Spec{Name: "k"}}}, upstream.FamilyMeta: {{Spec: upstream.Spec{Name: "m"}}}}}
	next := state.WithGroups(upstream.FamilyKiro, nil)
	if _, kept := next.Upstream[upstream.FamilyKiro]; kept || len(next.Upstream[upstream.FamilyMeta]) != 1 || len(state.Upstream[upstream.FamilyKiro]) != 1 || next.ClientKeys[0] != "c" {
		t.Fatalf("WithGroups = %v from %v", next, state)
	}
	keys := state.WithClientKeys(nil, nil)
	if len(keys.ClientKeys) != 0 || len(keys.Upstream) != 2 || len(state.ClientKeys) != 1 {
		t.Fatalf("WithClientKeys = %v from %v", keys, state)
	}
	if unnamed := state.WithClientKeys([]string{"a", "b"}, []string{"", ""}); unnamed.ClientKeyNames != nil {
		t.Fatalf("WithClientKeys kept empty names: %q", unnamed.ClientKeyNames)
	}
}
