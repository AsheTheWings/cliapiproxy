//go:build unit

package executor

import (
	"strings"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/tidwall/gjson"
)

const kiroTestSignedReasoning = `{"reasoningText":{"text":"thinking","signature":"sig-1"}}`

func kiroTestModel(t *testing.T, id string) *kiroauth.CatalogModel {
	t.Helper()
	const catalog = `{"models":[
	  {"modelId":"claude-haiku-5.5","tokenLimits":{"maxInputTokens":1000000},"additionalModelRequestFieldsSchema":{"properties":{
	    "thinking":{"properties":{"type":{"enum":["adaptive","disabled"]}}},
	    "output_config":{"properties":{"effort":{"enum":["low","medium","high","xhigh","max"],"default":"medium"}}}}}},
	  {"modelId":"gpt-5.6-luna","additionalModelRequestFieldsSchema":{"properties":{
	    "reasoning":{"properties":{"effort":{"enum":["none","low","medium","high"],"default":"high"}}}}}},
	  {"modelId":"kimi-k3","additionalModelRequestFieldsSchema":{"properties":{
	    "reasoning":{"properties":{"effort":{"enum":["low","high","max"],"default":"max"}}}}}},
	  {"modelId":"deepseek-3.2"}]}`
	parsed, err := kiroauth.ParseCatalog([]byte(catalog))
	if err != nil {
		t.Fatal(err)
	}
	model, ok := parsed.Model(id)
	if !ok {
		t.Fatalf("model %s missing", id)
	}
	return &model
}

func buildTestRequest(t *testing.T, body, model, session string) gjson.Result {
	t.Helper()
	out, err := buildKiroRequest(kiroRequestInput{
		Body: []byte(body), Model: model, ProfileARN: "arn:profile", SessionID: session,
		CatalogModel: kiroTestModel(t, model),
	})
	if err != nil {
		t.Fatalf("buildKiroRequest: %v", err)
	}
	if !gjson.ValidBytes(out) {
		t.Fatalf("request is not JSON: %s", out)
	}
	return gjson.ParseBytes(out)
}

func TestKiroRequestCarriesOnlyTheFieldsTheRouteAccepts(t *testing.T) {
	got := buildTestRequest(t, `{"model":"x","instructions":"be brief","input":"hello","temperature":0.2,"top_p":1,
	  "max_output_tokens":100,"tool_choice":"auto","parallel_tool_calls":true,"store":false,
	  "include":["reasoning.encrypted_content"],"prompt_cache_key":"k","service_tier":"auto","stream":false,
	  "stream_options":{"include_usage":true},"metadata":{"mine":"1"},"previous_response_id":"r"}`, "claude-haiku-5.5", "session-1")
	var keys []string
	got.ForEach(func(key, _ gjson.Result) bool {
		keys = append(keys, key.String())
		return true
	})
	if want := "model,input,stream,origin,profileArn,metadata,reasoning"; strings.Join(keys, ",") != want {
		t.Errorf("keys = %v, want %s", keys, want)
	}
	if !got.Get("stream").Bool() || got.Get("origin").String() != "AI_EDITOR" || got.Get("profileArn").String() != "arn:profile" {
		t.Errorf("envelope = %s", got.Raw)
	}
	if got.Get("metadata.mine").Exists() {
		t.Error("client metadata reached the service")
	}
}

func TestKiroRequestMovesSystemTextIntoALeadingUserTurn(t *testing.T) {
	got := buildTestRequest(t, `{"instructions":"rule one","input":[
	  {"type":"message","role":"developer","content":[{"type":"input_text","text":"rule two"}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
	  {"type":"message","role":"developer","content":[{"type":"input_text","text":"later note"}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}]}`, "deepseek-3.2", "s")
	input := got.Get("input").Array()
	if len(input) != 5 {
		t.Fatalf("input = %s", got.Get("input").Raw)
	}
	if input[0].Get("role").String() != "user" || input[0].Get("content").String() != "rule one\n\nrule two" {
		t.Errorf("system turn = %s", input[0].Raw)
	}
	if input[1].Get("role").String() != "assistant" || input[1].Get("content").String() != "I will follow these instructions." {
		t.Errorf("acknowledgement = %s", input[1].Raw)
	}
	if input[2].Get("content").String() != "hi" || input[3].Get("content").String() != "hello" {
		t.Errorf("conversation = %s", got.Get("input").Raw)
	}
	if want := "later note\n\nagain"; input[4].Get("role").String() != "user" || input[4].Get("content").String() != want {
		t.Errorf("late system text = %s, want a user turn %q", input[4].Raw, want)
	}
	if got.Get("reasoning").Exists() {
		t.Error("a model without an effort schema got a reasoning field")
	}
}

func TestKiroRequestKeepsOnlyFunctionTools(t *testing.T) {
	got := buildTestRequest(t, `{"input":"hi","tools":[
	  {"type":"function","name":"read","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}},"strict":true},
	  {"type":"web_search"},{"type":"function","name":"bare"},{"type":"custom","name":"apply_patch"}]}`, "deepseek-3.2", "s")
	tools := got.Get("tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools = %s", got.Get("tools").Raw)
	}
	if tools[0].Raw != `{"type":"function","name":"read","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}` {
		t.Errorf("tool = %s", tools[0].Raw)
	}
	if tools[1].Get("parameters").Raw != `{"type":"object","properties":{}}` {
		t.Errorf("tool without parameters = %s", tools[1].Raw)
	}
	if none := buildTestRequest(t, `{"input":"hi","tools":[{"type":"web_search"}]}`, "deepseek-3.2", "s"); none.Get("tools").Exists() {
		t.Errorf("tools = %s", none.Get("tools").Raw)
	}
}

func TestKiroRequestReasoningFollowsTheModelSchema(t *testing.T) {
	cases := []struct {
		name, model, reasoning, want string
	}{
		{"claude default", "claude-haiku-5.5", ``, `{"type":"adaptive","effort":"medium"}`},
		{"claude effort", "claude-haiku-5.5", `{"effort":"max"}`, `{"type":"adaptive","effort":"max"}`},
		{"claude off", "claude-haiku-5.5", `{"effort":"none"}`, `{"type":"disabled"}`},
		{"gpt default", "gpt-5.6-luna", ``, `{"effort":"high"}`},
		{"gpt effort", "gpt-5.6-luna", `{"effort":"low"}`, `{"effort":"low"}`},
		{"gpt off", "gpt-5.6-luna", `{"effort":"none"}`, `{"effort":"none"}`},
		{"gpt unknown level", "gpt-5.6-luna", `{"effort":"xhigh"}`, `{"effort":"high"}`},
		{"kimi cannot turn off", "kimi-k3", `{"effort":"none"}`, `{"effort":"max"}`},
		{"kimi level", "kimi-k3", `{"effort":"low"}`, `{"effort":"low"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"input":"hi"}`
			if tc.reasoning != "" {
				body = `{"input":"hi","reasoning":` + tc.reasoning + `}`
			}
			if got := buildTestRequest(t, body, tc.model, "s").Get("reasoning").Raw; got != tc.want {
				t.Errorf("reasoning = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKiroRequestWithoutACatalogSendsNoReasoning(t *testing.T) {
	out, err := buildKiroRequest(kiroRequestInput{Body: []byte(`{"input":"hi","reasoning":{"effort":"high"}}`), Model: "claude-haiku-5.5", ProfileARN: "arn:profile"})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "reasoning").Exists() {
		t.Errorf("request = %s", out)
	}
}

func TestKiroRequestReplaysOnlyReasoningTheServiceProduced(t *testing.T) {
	body := func(items ...string) string {
		return `{"input":[{"role":"user","content":"q"},` + strings.Join(items, ",") + `]}`
	}
	signed := `{"type":"reasoning","summary":[],"encrypted_content":` + string(kiroMarshal(kiroTestSignedReasoning)) + `}`
	foreign := `{"type":"reasoning","summary":[],"encrypted_content":"gAAAAABopaque"}`
	unsigned := `{"type":"reasoning","summary":[],"encrypted_content":"{\"reasoningText\":{\"text\":\"t\"}}"}`

	got := buildTestRequest(t, body(signed, signed, foreign, unsigned, `{"role":"assistant","content":"a"}`), "claude-haiku-5.5", "s")
	input := got.Get("input").Array()
	if len(input) != 3 || input[1].Get("type").String() != "reasoning" || input[1].Get("encrypted_content").String() != kiroTestSignedReasoning {
		t.Errorf("input = %s", got.Get("input").Raw)
	}
	if input[1].Get("summary").Raw != "[]" {
		t.Errorf("summary = %s", input[1].Get("summary").Raw)
	}
	plain := buildTestRequest(t, body(signed), "deepseek-3.2", "s")
	if len(plain.Get("input").Array()) != 1 {
		t.Errorf("a model without reasoning received a reasoning item: %s", plain.Get("input").Raw)
	}
}

func TestKiroRequestOrdersToolResultsByCall(t *testing.T) {
	got := buildTestRequest(t, `{"input":[{"role":"user","content":"q"},
	  {"type":"function_call","call_id":"b","name":"f","arguments":"{\"n\": 2}"},
	  {"type":"function_call_output","call_id":"b","output":"two"},
	  {"type":"function_call","call_id":"a","name":"f","arguments":{"n":1}},
	  {"type":"function_call_output","call_id":"a","output":"one"},
	  {"type":"function_call_output","call_id":"a","output":"again"},
	  {"type":"function_call_output","call_id":"ghost","output":"x"},
	  {"role":"user","content":"next"}]}`, "deepseek-3.2", "s")
	var order []string
	for _, item := range got.Get("input").Array() {
		order = append(order, item.Get("type").String()+":"+item.Get("call_id").String())
	}
	if want := ":,function_call:b,function_call:a,function_call_output:b,function_call_output:a,:"; strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	items := got.Get("input").Array()
	if items[1].Get("arguments").String() != `{"n": 2}` || items[2].Get("arguments").String() != `{"n":1}` {
		t.Errorf("arguments = %s, %s", items[1].Get("arguments").Raw, items[2].Get("arguments").Raw)
	}
	if items[4].Get("output").String() != "one" {
		t.Errorf("a repeated result replaced the first: %s", items[4].Raw)
	}
}

func TestKiroRequestImages(t *testing.T) {
	got := buildTestRequest(t, `{"input":[{"role":"user","content":[
	  {"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"high"}]}]}`, "claude-haiku-5.5", "s")
	content := got.Get("input.0.content").Array()
	if len(content) != 2 || content[0].Raw != `{"type":"input_text","text":"look"}` ||
		content[1].Raw != `{"type":"input_image","image_url":"data:image/png;base64,AAAA"}` {
		t.Errorf("content = %s", got.Get("input.0.content").Raw)
	}
	_, err := buildKiroRequest(kiroRequestInput{
		Body:  []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`),
		Model: "claude-haiku-5.5", ProfileARN: "arn:profile",
	})
	if err == nil || !strings.Contains(err.Error(), "data URIs") {
		t.Errorf("a remote image URL gave %v", err)
	}
	if _, err = buildKiroRequest(kiroRequestInput{Body: []byte(`{"input":[]}`), Model: "m", ProfileARN: "p"}); err == nil {
		t.Error("an empty input was accepted")
	}
}

func TestKiroRequestIdentifiersFollowTheSession(t *testing.T) {
	body := func(prompts int) string {
		items := make([]string, 0, prompts)
		for i := 0; i < prompts; i++ {
			items = append(items, `{"role":"user","content":"q"}`, `{"role":"assistant","content":"a"}`)
		}
		return `{"input":[` + strings.Join(items, ",") + `]}`
	}
	first := buildTestRequest(t, body(1), "deepseek-3.2", "session-1").Get("metadata")
	again := buildTestRequest(t, body(1), "deepseek-3.2", "session-1").Get("metadata")
	next := buildTestRequest(t, body(2), "deepseek-3.2", "session-1").Get("metadata")
	other := buildTestRequest(t, body(1), "deepseek-3.2", "session-2").Get("metadata")

	if first.Raw != again.Raw {
		t.Errorf("the same prompt of a session changed its identifiers: %s vs %s", first.Raw, again.Raw)
	}
	if first.Get("kiro:conversation_id").String() != next.Get("kiro:conversation_id").String() {
		t.Error("a later prompt changed the conversation id")
	}
	if first.Get("kiro:agent_continuation_id").String() == next.Get("kiro:agent_continuation_id").String() {
		t.Error("a later prompt kept the continuation id")
	}
	if first.Get("kiro:conversation_id").String() == other.Get("kiro:conversation_id").String() {
		t.Error("two sessions share a conversation id")
	}
	if id := first.Get("kiro:conversation_id").String(); !strings.HasPrefix(id, "sess_") || first.Get("kiro:root_conversation_id").String() != id {
		t.Errorf("conversation ids = %s", first.Raw)
	}
	if first.Get("kiro:agent_mode").String() != "vibe" {
		t.Errorf("agent mode = %s", first.Raw)
	}
	one := buildTestRequest(t, body(1), "deepseek-3.2", "").Get("metadata.kiro:conversation_id").String()
	two := buildTestRequest(t, body(1), "deepseek-3.2", "").Get("metadata.kiro:conversation_id").String()
	if one == two {
		t.Error("requests without a session share a conversation id")
	}
}

func TestKiroRequestDropsToolCallsWithoutResults(t *testing.T) {
	got := buildTestRequest(t, `{"input":[{"role":"user","content":"q"},
	  {"type":"function_call","call_id":"a","name":"f","arguments":"{}"},
	  {"role":"user","content":"next"},
	  {"type":"function_call","call_id":"b","name":"f","arguments":"{}"},
	  {"type":"function_call_output","call_id":"b","output":"two"},
	  {"type":"function_call","call_id":"c","name":"f","arguments":"{}"},
	  {"role":"user","content":"last"}]}`, "deepseek-3.2", "s")
	var order []string
	for _, item := range got.Get("input").Array() {
		order = append(order, item.Get("type").String()+item.Get("role").String()+":"+item.Get("call_id").String())
	}
	if want := "user:,function_call:b,function_call_output:b,user:"; strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	if first := got.Get("input.0.content").String(); first != "q\n\nnext" {
		t.Errorf("messages around a dropped call = %q, want them merged", first)
	}
	if last := got.Get("input.3.content").String(); last != "last" {
		t.Errorf("last message = %q", last)
	}
}
