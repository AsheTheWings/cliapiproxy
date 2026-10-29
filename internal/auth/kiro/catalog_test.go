//go:build unit

package kiro

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// catalogJSON has the shape of a ListAvailableModels answer: one model per
// reasoning style and one without.
const catalogJSON = `{
  "defaultModel": {"modelId": "auto"},
  "models": [
    {"modelId": "claude-haiku-5.5", "modelName": "claude-haiku-5.5", "description": "fast",
     "rateMultiplier": 0.06, "rateUnit": "Credit", "supportedInputTypes": ["TEXT", "IMAGE"],
     "tokenLimits": {"maxInputTokens": 1000000, "maxOutputTokens": 64000},
     "promptCaching": {"supportsPromptCaching": true},
     "additionalModelRequestFieldsSchema": {"type": "object", "properties": {
       "thinking": {"type": "object", "properties": {"type": {"type": "string", "enum": ["adaptive", "disabled"]}}},
       "output_config": {"type": "object", "properties": {"effort": {"type": "string",
         "enum": ["low", "medium", "high", "xhigh", "max"], "default": "medium"}}}}}},
    {"modelId": "gpt-5.6-luna", "modelName": "gpt-5.6-luna", "supportedInputTypes": ["TEXT", "IMAGE"],
     "tokenLimits": {"maxInputTokens": 986000, "maxOutputTokens": 64000},
     "promptCaching": {"supportsPromptCaching": true},
     "additionalModelRequestFieldsSchema": {"type": "object", "properties": {
       "reasoning": {"type": "object", "properties": {"effort": {"type": "string",
         "enum": ["none", "low", "medium", "high", "xhigh", "max"], "default": "high"}}}}}},
    {"modelId": "kimi-k3", "supportedInputTypes": ["TEXT", "IMAGE"],
     "tokenLimits": {"maxInputTokens": 1000000, "maxOutputTokens": 64000},
     "additionalModelRequestFieldsSchema": {"type": "object", "properties": {
       "reasoning": {"type": "object", "properties": {"effort": {"type": "string",
         "enum": ["low", "high", "max"], "default": "max"}}}}}},
    {"modelId": "deepseek-3.2", "supportedInputTypes": ["TEXT"],
     "tokenLimits": {"maxInputTokens": 164000, "maxOutputTokens": 64000},
     "promptCaching": {"supportsPromptCaching": false}},
    {"modelId": "deepseek-3.2", "supportedInputTypes": ["TEXT"]},
    {"modelId": ""}
  ]
}`

func TestParseCatalogKeepsEachModelOnce(t *testing.T) {
	catalog, err := ParseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.DefaultModel != "auto" {
		t.Errorf("default model = %q, want auto", catalog.DefaultModel)
	}
	var ids []string
	for _, model := range catalog.Models {
		ids = append(ids, model.ModelID)
	}
	if want := []string{"claude-haiku-5.5", "gpt-5.6-luna", "kimi-k3", "deepseek-3.2"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("model ids = %v, want %v", ids, want)
	}
}

func TestParseCatalogRejectsEmptyAndMalformed(t *testing.T) {
	for _, body := range []string{`{"models": []}`, `{"models": [{"modelId": ""}]}`, `not json`} {
		if _, err := ParseCatalog([]byte(body)); err == nil {
			t.Errorf("ParseCatalog(%q) succeeded", body)
		}
	}
}

func TestReasoningFollowsTheModelSchema(t *testing.T) {
	catalog, err := ParseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		model string
		want  Reasoning
	}{
		{"claude-haiku-5.5", Reasoning{Style: ReasoningAdaptive, Levels: []string{"low", "medium", "high", "xhigh", "max"}, Default: "medium", CanDisable: true}},
		{"gpt-5.6-luna", Reasoning{Style: ReasoningEffort, Levels: []string{"low", "medium", "high", "xhigh", "max"}, Default: "high", CanDisable: true}},
		{"kimi-k3", Reasoning{Style: ReasoningEffort, Levels: []string{"low", "high", "max"}, Default: "max"}},
		{"deepseek-3.2", Reasoning{}},
	}
	for _, tc := range cases {
		model, ok := catalog.Model(tc.model)
		if !ok {
			t.Fatalf("model %s missing", tc.model)
		}
		if got := model.Reasoning(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s reasoning = %+v, want %+v", tc.model, got, tc.want)
		}
	}
}

func TestModelInfoUsesThePublicPrefixAndCatalogLimits(t *testing.T) {
	catalog, err := ParseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	infos := catalog.ModelInfos()
	if len(infos) != 4 {
		t.Fatalf("got %d model infos, want 4", len(infos))
	}
	haiku := infos[0]
	if haiku.ID != "kiro/claude-haiku-5.5" || haiku.Type != "kiro" || haiku.ContextLength != 1000000 || haiku.MaxCompletionTokens != 64000 {
		t.Errorf("unexpected model info: %+v", haiku)
	}
	if !reflect.DeepEqual(haiku.SupportedInputModalities, []string{"text", "image"}) {
		t.Errorf("input modalities = %v", haiku.SupportedInputModalities)
	}
	if haiku.Thinking == nil || !haiku.Thinking.ZeroAllowed || len(haiku.Thinking.Levels) != 5 {
		t.Errorf("thinking = %+v", haiku.Thinking)
	}
	if kimi := infos[2]; kimi.Thinking == nil || kimi.Thinking.ZeroAllowed {
		t.Errorf("kimi thinking = %+v, want levels without zero", kimi.Thinking)
	}
	if deepseek := infos[3]; deepseek.Thinking != nil {
		t.Errorf("deepseek thinking = %+v, want none", deepseek.Thinking)
	}
	if got := UpstreamModel("kiro/claude-haiku-5.5"); got != "claude-haiku-5.5" {
		t.Errorf("UpstreamModel = %q", got)
	}
}

func TestCatalogSurvivesTheCredentialFile(t *testing.T) {
	catalog, err := ParseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := catalog.Metadata(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	file, err := json.Marshal(map[string]any{CatalogMetadataKey: stored})
	if err != nil {
		t.Fatal(err)
	}
	var loaded map[string]any
	if err = json.Unmarshal(file, &loaded); err != nil {
		t.Fatal(err)
	}
	got, ok := CatalogFromMetadata(loaded)
	if !ok {
		t.Fatal("catalog missing after the round trip")
	}
	if len(got.Models) != len(catalog.Models) {
		t.Errorf("catalog changed: %d models, want %d", len(got.Models), len(catalog.Models))
	}
	model, _ := got.Model("kimi-k3")
	if model.Reasoning().Default != "max" {
		t.Errorf("schema lost in the round trip: %+v", model.Reasoning())
	}
	if _, ok := CatalogFromMetadata(map[string]any{}); ok {
		t.Error("an empty credential has a catalog")
	}
}
