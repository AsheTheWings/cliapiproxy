//go:build unit

package upstream

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseStateRoundTripsItsEncoding(t *testing.T) {
	state := State{
		FamilyOpenAICompatibility: {{Spec: Spec{Name: "alpha", BaseURL: "https://alpha.invalid/v1", Models: []Model{{Name: "m"}}}, Keys: []Key{{APIKey: "k1"}, {APIKey: "k2"}}}},
		FamilyKiro:                {{Spec: Spec{Name: "kiro", Prefix: "k"}, Keys: []Key{{APIKey: "k3"}}}},
	}
	data, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseState(data)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := parsed.Encode()
	if string(again) != string(data) {
		t.Fatalf("round trip changed the state:\n%s\nwant\n%s", again, data)
	}
	if empty, err := ParseState([]byte("\n")); err != nil || len(empty) != 0 {
		t.Fatalf("empty file = %v, %v; want the empty state", empty, err)
	}
}

func TestParseStateRejectsBrokenGroupsWithoutNamingKeys(t *testing.T) {
	for name, data := range map[string]string{
		"unknown family":   "api-keys: {gemini: [{name: g, keys: [{api-key: secret-x}]}]}",
		"unknown field":    "api-keys: {kiro: [{name: k, region: eu, keys: [{api-key: secret-x}]}]}",
		"no key":           "api-keys: {kiro: [{name: k, keys: []}]}",
		"empty key":        "api-keys: {kiro: [{name: k, keys: [{api-key: ''}]}]}",
		"repeated key":     "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}, {api-key: secret-x}]}]}",
		"base-url on kiro": "api-keys: {kiro: [{name: k, base-url: 'https://k.invalid', keys: [{api-key: secret-x}]}]}",
		"no models":        "api-keys: {openai-compatibility: [{name: a, base-url: 'https://a.invalid', keys: [{api-key: secret-x}]}]}",
		"twice":            "api-keys: {kiro: [{name: k, keys: [{api-key: secret-x}]}, {name: k, keys: [{api-key: secret-y}]}]}",
	} {
		_, err := ParseState([]byte(data))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Errorf("%s: error names a key: %v", name, err)
		}
	}
}

func TestMergeDocumentAppendsStateGroups(t *testing.T) {
	document := []byte("config-version: 8\napi-keys:\n  kiro:\n    - {name: inputs, keys: [{api-key: k1}]}\n")
	state := State{
		FamilyKiro:        {{Spec: Spec{Name: "state"}, Keys: []Key{{APIKey: "k2"}}}},
		FamilyCommandCode: {{Spec: Spec{Name: "cc"}, Keys: []Key{{APIKey: "k3"}}}},
	}
	merged, err := MergeDocument(document, state)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := DocumentGroups(merged)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, family := range []string{FamilyKiro, FamilyCommandCode} {
		for _, g := range groups[family] {
			names = append(names, family+"/"+g.Name+"="+g.Keys[0].APIKey)
		}
	}
	if got := strings.Join(names, " "); got != "kiro/inputs=k1 kiro/state=k2 commandcode/cc=k3" {
		t.Fatalf("groups = %s", got)
	}
	var root map[string]any
	if err = yaml.Unmarshal(merged, &root); err != nil || root["config-version"] != 8 {
		t.Fatalf("merged document lost its other keys: %v %v", root, err)
	}
	if _, err = MergeDocument(document, State{FamilyKiro: {{Spec: Spec{Name: "inputs"}, Keys: []Key{{APIKey: "k4"}}}}}); err == nil {
		t.Fatal("a state group named like an inputs group merged")
	}
}

func TestWithReplacesOneFamily(t *testing.T) {
	state := State{FamilyKiro: {{Spec: Spec{Name: "k"}}}, FamilyMeta: {{Spec: Spec{Name: "m"}}}}
	next := state.With(FamilyKiro, nil)
	if _, kept := next[FamilyKiro]; kept || len(next[FamilyMeta]) != 1 || len(state[FamilyKiro]) != 1 {
		t.Fatalf("With = %v from %v", next, state)
	}
}
