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
		ClientKeys: []upstream.Key{{APIKey: "client-1", Label: "Alice's laptop"}, {APIKey: "client-2"}},
		Upstream: map[string][]upstream.Group{
			upstream.FamilyOpenAICompatibility: {{Spec: upstream.Spec{Name: "alpha", BaseURL: "https://alpha.invalid/v1", Models: []upstream.Model{{Name: "m"}}}, Keys: []upstream.Key{{APIKey: "k1", Label: "team"}, {APIKey: "k2"}}}},
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
	if parsed.ClientKeys[0].Label != "Alice's laptop" || parsed.Upstream[upstream.FamilyOpenAICompatibility][0].Keys[0].Label != "team" {
		t.Fatalf("labels lost in the round trip: %+v", parsed)
	}
	if empty, err := Parse([]byte("\n")); err != nil || len(empty.ClientKeys)+len(empty.Upstream) != 0 {
		t.Fatalf("empty file = %v, %v; want the empty state", empty, err)
	}
}

func TestParseRejectsBrokenKeysWithoutNamingThem(t *testing.T) {
	for name, tc := range map[string]struct{ data, position string }{
		"unknown family":      {data: "api-keys: {gemini: [{name: g, keys: [{api-key: secret-x}]}]}"},
		"unknown field":       {data: "api-keys: {kiro: [{name: k, region: eu, keys: [{api-key: secret-x}]}]}"},
		"unknown key field":   {data: "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x, name: n}]}]}"},
		"unknown section":     {data: "routing: {strategy: fill-first}"},
		"no key":              {data: "api-keys: {kiro: [{name: k, keys: []}]}"},
		"empty key":           {data: "api-keys: {kiro: [{name: k, keys: [{api-key: ''}]}]}", position: "key 1"},
		"repeated key":        {data: "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}, {api-key: secret-x}]}]}", position: "key 2"},
		"base-url on kiro":    {data: "api-keys: {kiro: [{name: k, base-url: 'https://k.invalid', keys: [{api-key: secret-x}]}]}"},
		"no models":           {data: "api-keys: {openai-compatibility: [{name: a, base-url: 'https://a.invalid', keys: [{api-key: secret-x}]}]}"},
		"twice":               {data: "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}]}, {name: k, keys: [{api-key: secret-y}]}]}"},
		"client key string":   {data: "access: {api-keys: [secret-c]}"},
		"client key names":    {data: "access: {api-keys: [{api-key: secret-c}], api-key-names: [a]}"},
		"empty client key":    {data: "access: {api-keys: [{api-key: ''}]}", position: "key 1"},
		"blank client key":    {data: "access: {api-keys: [{api-key: ' '}]}", position: "key 1"},
		"padded client key":   {data: "access: {api-keys: [{api-key: ' secret-c'}]}", position: "key 1"},
		"padded group key":    {data: "api-keys: {kiro: [{name: k, keys: [{api-key: 'secret-x '}]}]}", position: "key 1"},
		"repeated client key": {data: "access: {api-keys: [{api-key: secret-c}, {api-key: secret-c}]}", position: "key 2"},
		"label too long":      {data: "access: {api-keys: [{api-key: secret-c, label: " + strings.Repeat("n", upstream.MaxLabelLength+1) + "}]}", position: "access.api-keys: key 1"},
		"label with newline":  {data: "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x, label: \"a\\nb\"}]}]}", position: "api-keys.kiro: group k key 1"},
		"label padded":        {data: "access: {api-keys: [{api-key: secret-c, label: ' a'}]}", position: "access.api-keys: key 1"},
		"client label repeated": {
			data:     "access: {api-keys: [{api-key: secret-c, label: a}, {api-key: secret-d, label: a}]}",
			position: "access.api-keys: key 2: label repeats the label of key 1",
		},
		"family label repeated": {
			data:     "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x, label: a}]}, {name: j, keys: [{api-key: secret-y}, {api-key: secret-z, label: a}]}]}",
			position: "api-keys.kiro: group j key 2: label repeats the label of group k key 1",
		},
	} {
		_, err := Parse([]byte(tc.data))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Errorf("%s: error names a key: %v", name, err)
		}
		if !strings.Contains(err.Error(), tc.position) {
			t.Errorf("%s: error %q does not name %q", name, err, tc.position)
		}
	}
}

func TestLabelsAreUniquePerFamilyAndAmongClientKeys(t *testing.T) {
	// The same label in two families, and on a client key, names different
	// accounts and is accepted.
	data := "access: {api-keys: [{api-key: c1, label: shared}]}\n" +
		"api-keys:\n" +
		"  kiro: [{name: k, keys: [{api-key: k1, label: shared}]}]\n" +
		"  commandcode: [{name: c, keys: [{api-key: k2, label: shared}]}]\n"
	if _, err := Parse([]byte(data)); err != nil {
		t.Fatalf("a label shared across scopes: %v", err)
	}
}

func TestMergeAddsTheStateAsTheOnlySourceOfKeys(t *testing.T) {
	document := []byte("config-version: 8\nrouting: {strategy: fill-first}\n")
	state := State{
		ClientKeys: []upstream.Key{{APIKey: "client-1", Label: "ci runner"}},
		Upstream:   map[string][]upstream.Group{upstream.FamilyKiro: {{Spec: upstream.Spec{Name: "k"}, Keys: []upstream.Key{{APIKey: "k1", Label: "team"}}}}},
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
		t.Fatalf("merged document holds a client key label: %s", merged)
	}
	for _, declared := range []string{"api-keys: {kiro: []}\n", "access: {api-keys: [k]}\n"} {
		if _, err = Merge([]byte(declared), state); err == nil {
			t.Errorf("a document declaring %q merged", declared)
		}
	}
}

func TestWithReplacesOnePart(t *testing.T) {
	state := State{ClientKeys: []upstream.Key{{APIKey: "c"}}, Upstream: map[string][]upstream.Group{upstream.FamilyKiro: {{Spec: upstream.Spec{Name: "k"}}}, upstream.FamilyMeta: {{Spec: upstream.Spec{Name: "m"}}}}}
	next := state.WithGroups(upstream.FamilyKiro, nil)
	if _, kept := next.Upstream[upstream.FamilyKiro]; kept || len(next.Upstream[upstream.FamilyMeta]) != 1 || len(state.Upstream[upstream.FamilyKiro]) != 1 || next.ClientKeys[0].APIKey != "c" {
		t.Fatalf("WithGroups = %v from %v", next, state)
	}
	keys := state.WithClientKeys(nil)
	if len(keys.ClientKeys) != 0 || len(keys.Upstream) != 2 || len(state.ClientKeys) != 1 {
		t.Fatalf("WithClientKeys = %v from %v", keys, state)
	}
}
