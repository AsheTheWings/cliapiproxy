//go:build unit

package commandcode

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// catalogJSON has the shape of the client view of the model route.
const catalogJSON = `{
  "catalogVersion": "v1",
  "data": [
    {"id": "reflection/beam:free", "name": "Beam", "category": "opensource", "minimumPlan": "individual-go",
     "context_length": 200000, "maxOutputTokens": 32768, "inputModalities": ["text"], "reasoningEfforts": ["low", "high"]},
    {"id": "deepseek/deepseek-v4-flash", "name": "DeepSeek V4 Flash", "category": "opensource", "minimumPlan": "individual-go",
     "context_length": 1000000, "maxOutputTokens": null, "inputModalities": ["text", "image"], "reasoningEfforts": ["off", "low", "medium", "high"]},
    {"id": "xiaomi/mimo-v2.6-pro-ultraspeed", "name": "MiMo", "category": "opensource", "minimumPlan": "individual-goat",
     "context_length": 1048576, "inputModalities": ["text"], "reasoningEfforts": null},
    {"id": "gpt-6-luna", "name": "GPT-6 Luna", "category": "premium", "minimumPlan": "individual-pro",
     "context_length": 400000, "inputModalities": ["text", "image"], "reasoningEfforts": ["low", "xhigh"]},
    {"id": "no-plan-model", "name": "Open", "category": "opensource"},
    {"id": "other-category", "category": "stealth", "minimumPlan": "individual-max"},
    {"id": "reflection/beam:free"},
    {"id": ""}
  ]
}`

func parsedCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := ParseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func ids(models []CatalogModel) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.ID)
	}
	return out
}

func TestParseCatalogKeepsEachModelOnce(t *testing.T) {
	catalog := parsedCatalog(t)
	if catalog.Version != "v1" {
		t.Errorf("version = %q", catalog.Version)
	}
	want := []string{"reflection/beam:free", "deepseek/deepseek-v4-flash", "xiaomi/mimo-v2.6-pro-ultraspeed", "gpt-6-luna", "no-plan-model", "other-category"}
	if got := ids(catalog.Models); !reflect.DeepEqual(got, want) {
		t.Errorf("model ids = %v, want %v", got, want)
	}
}

func TestParseCatalogRejectsEmptyAndMalformed(t *testing.T) {
	for _, body := range []string{`{"data": []}`, `{"data": [{"id": ""}]}`, `not json`} {
		if _, err := ParseCatalog([]byte(body)); err == nil {
			t.Errorf("ParseCatalog(%q) succeeded", body)
		}
	}
}

func TestAccountAllowsModelsByPlanRankAndCredits(t *testing.T) {
	catalog := parsedCatalog(t)
	cases := []struct {
		name    string
		account Account
		want    []string
	}{
		{"go plan", Account{PlanID: "individual-go"},
			[]string{"reflection/beam:free", "deepseek/deepseek-v4-flash", "no-plan-model", "other-category"}},
		{"goat plan", Account{PlanID: "individual-goat"},
			[]string{"reflection/beam:free", "deepseek/deepseek-v4-flash", "xiaomi/mimo-v2.6-pro-ultraspeed", "no-plan-model", "other-category"}},
		{"pro plan", Account{PlanID: "individual-pro"},
			[]string{"reflection/beam:free", "deepseek/deepseek-v4-flash", "xiaomi/mimo-v2.6-pro-ultraspeed", "gpt-6-luna", "no-plan-model", "other-category"}},
		{"unknown plan", Account{PlanID: "individual-future"}, ids(catalog.Models)},
		{"no plan", Account{}, ids(catalog.Models)},
		{"go plan with purchased credits", Account{PlanID: "individual-go", PurchasedCredits: 1}, ids(catalog.Models)},
		{"go plan with free credits", Account{PlanID: "individual-go", FreeCredits: 0.5}, ids(catalog.Models)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog.Account = tc.account
			if got := ids(catalog.Available()); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("available = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAccountFallsBackToCategoryForUnrankedMinimumPlans(t *testing.T) {
	account := Account{PlanID: "individual-go"}
	if !account.Allows(CatalogModel{ID: "m", Category: "opensource", MinimumPlan: "individual-unranked"}) {
		t.Error("an open model with an unranked minimum plan is refused to a plan that includes open models")
	}
	if account.Allows(CatalogModel{ID: "m", Category: "premium", MinimumPlan: "individual-unranked"}) {
		t.Error("a premium model with an unranked minimum plan is allowed to a plan without premium models")
	}
}

func TestModelInfoUsesThePublicPrefixAndCatalogLimits(t *testing.T) {
	catalog := parsedCatalog(t)
	catalog.Account = Account{PlanID: "individual-go"}
	infos := catalog.ModelInfos()
	if len(infos) != 4 {
		t.Fatalf("got %d model infos, want 4", len(infos))
	}
	beam, flash := infos[0], infos[1]
	if beam.ID != "commandcode/reflection/beam:free" || beam.Type != "commandcode" || beam.ContextLength != 200000 || beam.MaxCompletionTokens != 32768 {
		t.Errorf("unexpected model info: %+v", beam)
	}
	if beam.Thinking == nil || beam.Thinking.ZeroAllowed || !reflect.DeepEqual(beam.Thinking.Levels, []string{"low", "high"}) {
		t.Errorf("beam thinking = %+v", beam.Thinking)
	}
	if flash.Thinking == nil || !flash.Thinking.ZeroAllowed || !reflect.DeepEqual(flash.Thinking.Levels, []string{"low", "medium", "high"}) {
		t.Errorf("flash thinking = %+v", flash.Thinking)
	}
	if !reflect.DeepEqual(flash.SupportedInputModalities, []string{"text", "image"}) {
		t.Errorf("modalities = %v", flash.SupportedInputModalities)
	}
	if infos[2].Thinking != nil {
		t.Errorf("a model without efforts has thinking: %+v", infos[2].Thinking)
	}
	if got := UpstreamModel("commandcode/Qwen/Qwen3.7-Flash"); got != "Qwen/Qwen3.7-Flash" {
		t.Errorf("UpstreamModel = %q", got)
	}
}

func TestCatalogSurvivesTheCredentialFile(t *testing.T) {
	catalog := parsedCatalog(t)
	catalog.Account = Account{UserID: "u1", PlanID: "individual-goat", PurchasedCredits: 2}
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
	if len(got.Models) != len(catalog.Models) || got.Account != catalog.Account || got.Version != "v1" {
		t.Errorf("catalog changed: %+v", got)
	}
	model, _ := got.Model("gpt-6-luna")
	if levels, _ := model.Efforts(); !reflect.DeepEqual(levels, []string{"low", "xhigh"}) {
		t.Errorf("efforts lost in the round trip: %v", levels)
	}
	if _, ok := CatalogFromMetadata(map[string]any{}); ok {
		t.Error("an empty credential has a catalog")
	}
}
