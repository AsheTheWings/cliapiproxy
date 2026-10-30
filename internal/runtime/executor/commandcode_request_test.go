//go:build unit

package executor

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	commandcodeauth "github.com/AsheTheWings/cliapiproxy/internal/auth/commandcode"
	"github.com/tidwall/gjson"
)

var commandCodeTestNow = time.Date(2026, 10, 29, 23, 30, 0, 0, time.UTC)

func buildCommandCodeTest(t *testing.T, body string, model *commandcodeauth.CatalogModel, session string) gjson.Result {
	t.Helper()
	request, err := buildCommandCodeRequest(commandCodeRequestInput{
		Body: []byte(body), Model: "deepseek/deepseek-v4-flash", SessionID: session, CatalogModel: model, Now: commandCodeTestNow,
		MaxTokens: gjson.Get(body, "max_output_tokens").Int(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.ValidBytes(request.Body) {
		t.Fatalf("invalid body: %s", request.Body)
	}
	parsed := gjson.ParseBytes(request.Body)
	if parsed.Get("threadId").String() != request.ThreadID {
		t.Fatalf("thread id %q differs from the body's %q", request.ThreadID, parsed.Get("threadId").String())
	}
	return parsed
}

func TestCommandCodeRequestCarriesTheCLIEnvelope(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":"hi","instructions":"Be brief."}`, nil, "")
	config := got.Get("config")
	if config.Get("workingDir").String() != "/work" || config.Get("date").String() != "2026-10-29" ||
		config.Get("environment").String() != "linux" || config.Get("isGitRepo").Bool() ||
		!config.Get("structure").IsArray() || !config.Get("recentCommits").IsArray() {
		t.Errorf("config = %s", config.Raw)
	}
	for _, field := range []string{"memory", "taste", "skills"} {
		if value := got.Get(field); !value.Exists() || value.Type != gjson.Null {
			t.Errorf("%s = %s, want null", field, value.Raw)
		}
	}
	if got.Get("permissionMode").String() != "standard" {
		t.Errorf("permissionMode = %s", got.Get("permissionMode").Raw)
	}
	params := got.Get("params")
	if params.Get("model").String() != "deepseek/deepseek-v4-flash" || !params.Get("stream").Bool() ||
		params.Get("max_tokens").Int() != 64000 || !params.Get("tools").IsArray() || params.Get("temperature").Exists() {
		t.Errorf("params = %s", params.Raw)
	}
	system := params.Get("system.0")
	if system.Get("text").String() != "Be brief." || system.Get("cache_control.type").String() != "ephemeral" {
		t.Errorf("system = %s", system.Raw)
	}
}

func TestCommandCodeRequestWithoutSystemTextReplacesTheVendorPrompt(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":"hi"}`, nil, "")
	if text := got.Get("params.system.0.text").String(); text != commandCodeDefaultSystem {
		t.Errorf("system text = %q", text)
	}
}

func TestCommandCodeRequestJoinsLeadingSystemAndDeveloperText(t *testing.T) {
	got := buildCommandCodeTest(t, `{"instructions":"A","input":[
		{"role":"developer","content":[{"type":"input_text","text":"B"}]},
		{"role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"role":"system","content":[{"type":"input_text","text":"C"}]}]}`, nil, "")
	if got.Get("params.system.0.text").String() != "A\n\nB" {
		t.Errorf("system = %s", got.Get("params.system").Raw)
	}
	messages := got.Get("params.messages")
	if len(messages.Array()) != 1 || messages.Get("0.content.#").Int() != 2 || messages.Get("0.content.1.text").String() != "C" {
		t.Errorf("a later system message must join the user turn: %s", messages.Raw)
	}
}

func TestCommandCodeRequestGroupsToolRoundTrips(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":[
		{"role":"user","content":[{"type":"input_text","text":"list"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking."}]},
		{"type":"function_call","call_id":"c1","name":"ls","arguments":"{\"path\":\".\"}"},
		{"type":"function_call","call_id":"c2","name":"pwd","arguments":"{}"},
		{"type":"function_call_output","call_id":"c2","output":"/work"},
		{"type":"function_call_output","call_id":"c1","output":"a b"},
		{"role":"user","content":[{"type":"input_text","text":"more"}]}]}`, nil, "")
	messages := got.Get("params.messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages = %s", got.Get("params.messages").Raw)
	}
	assistant := messages[1].Get("content").Array()
	kinds := make([]string, len(assistant))
	for i, part := range assistant {
		kinds[i] = part.Get("type").String()
	}
	if strings.Join(kinds, ",") != "reasoning,text,tool-call,tool-call" || assistant[0].Get("text").String() != "thinking" {
		t.Errorf("assistant parts = %s", messages[1].Raw)
	}
	if assistant[2].Get("toolCallId").String() != "c1" || assistant[2].Get("input.path").String() != "." {
		t.Errorf("tool call = %s", assistant[2].Raw)
	}
	tool := messages[2]
	if tool.Get("role").String() != "tool" || tool.Get("content.#").Int() != 2 ||
		tool.Get("content.0.toolCallId").String() != "c1" || tool.Get("content.0.toolName").String() != "ls" ||
		tool.Get("content.0.output.type").String() != "text" || tool.Get("content.0.output.value").String() != "a b" {
		t.Errorf("tool results must follow call order as typed outputs: %s", tool.Raw)
	}
	if messages[3].Get("role").String() != "user" {
		t.Errorf("last message = %s", messages[3].Raw)
	}
}

func TestCommandCodeRequestDropsUnpairedToolItems(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":[
		{"role":"user","content":"go"},
		{"type":"function_call","call_id":"lost","name":"ls","arguments":"{}"},
		{"type":"function_call_output","call_id":"orphan","output":"x"},
		{"type":"function_call","call_id":"ok","name":"ls","arguments":"{}"},
		{"type":"function_call_output","call_id":"ok","output":"y"},
		{"type":"function_call_output","call_id":"ok","output":"again"}]}`, nil, "")
	raw := got.Get("params.messages").Raw
	if strings.Contains(raw, "lost") || strings.Contains(raw, "orphan") || strings.Contains(raw, "again") {
		t.Errorf("unpaired items were sent: %s", raw)
	}
	if got.Get("params.messages.#").Int() != 3 {
		t.Errorf("messages = %s", raw)
	}
}

func TestCommandCodeRequestAliasesLongToolCallIDs(t *testing.T) {
	long := strings.Repeat("x", 65)
	short := strings.Repeat("y", 64)
	got := buildCommandCodeTest(t, `{"input":[{"role":"user","content":"go"},
		{"type":"function_call","call_id":"`+long+`","name":"a","arguments":"{}"},
		{"type":"function_call","call_id":"`+short+`","name":"b","arguments":"{}"},
		{"type":"function_call_output","call_id":"`+long+`","output":"1"},
		{"type":"function_call_output","call_id":"`+short+`","output":"2"}]}`, nil, "")
	if got.Get("params.messages.1.content.0.toolCallId").String() != "cc-1" ||
		got.Get("params.messages.2.content.0.toolCallId").String() != "cc-1" {
		t.Errorf("long id must be aliased consistently: %s", got.Get("params.messages").Raw)
	}
	if got.Get("params.messages.1.content.1.toolCallId").String() != short {
		t.Errorf("a 64-character id must stay: %s", got.Get("params.messages.1").Raw)
	}
}

func TestCommandCodeRequestAliasesSkipIDsTheConversationUses(t *testing.T) {
	long := strings.Repeat("x", 65)
	got := buildCommandCodeTest(t, `{"input":[{"role":"user","content":"go"},
		{"type":"function_call","call_id":"`+long+`","name":"a","arguments":"{}"},
		{"type":"function_call","call_id":"cc-1","name":"b","arguments":"{}"},
		{"type":"function_call_output","call_id":"`+long+`","output":"1"},
		{"type":"function_call_output","call_id":"cc-1","output":"2"}]}`, nil, "")
	calls := got.Get("params.messages.1.content.#.toolCallId").Array()
	results := got.Get("params.messages.2.content.#.toolCallId").Array()
	if len(calls) != 2 || len(results) != 2 || calls[0].String() != "cc-2" || calls[1].String() != "cc-1" ||
		results[0].String() != "cc-2" || results[1].String() != "cc-1" {
		t.Errorf("ids = calls %v, results %v; want cc-2 for the long id and cc-1 kept", calls, results)
	}
}

func TestCommandCodeRequestReplaysReasoningFromTheEnvelopeOrSummary(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":[{"role":"user","content":"go"},
		{"type":"reasoning","summary":[],"encrypted_content":"{\"commandcode_reasoning\":\"deep thoughts\"}"},
		{"type":"reasoning","summary":[],"encrypted_content":"opaque-from-another-provider"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`, nil, "")
	assistant := got.Get("params.messages.1.content").Array()
	if len(assistant) != 2 || assistant[0].Get("type").String() != "reasoning" || assistant[0].Get("text").String() != "deep thoughts" {
		t.Errorf("assistant = %s", got.Get("params.messages.1").Raw)
	}
}

func TestCommandCodeRequestImages(t *testing.T) {
	body := `{"input":[{"role":"user","content":[{"type":"input_text","text":"see"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`
	sees := &commandcodeauth.CatalogModel{ID: "m", InputModalities: []string{"text", "image"}}
	got := buildCommandCodeTest(t, body, sees, "")
	image := got.Get("params.messages.0.content.1")
	if image.Get("type").String() != "image" || image.Get("image").String() != "data:image/png;base64,AAAA" || image.Get("mimeType").String() != "image/png" {
		t.Errorf("image = %s", image.Raw)
	}
	blind := &commandcodeauth.CatalogModel{ID: "m", InputModalities: []string{"text"}}
	got = buildCommandCodeTest(t, body, blind, "")
	if got.Get("params.messages.0.content.1.text").String() != commandCodeImageOmitted || strings.Contains(got.Raw, "AAAA") {
		t.Errorf("a text-only model received the image: %s", got.Get("params.messages").Raw)
	}
	_, err := buildCommandCodeRequest(commandCodeRequestInput{
		Body: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`), Model: "m",
	})
	var status statusErr
	if !errors.As(err, &status) || status.code != http.StatusBadRequest {
		t.Errorf("a remote image gave %v", err)
	}
}

func TestCommandCodeRequestSendsOnlyEffortsTheModelTakes(t *testing.T) {
	model := &commandcodeauth.CatalogModel{ID: "m", ReasoningEfforts: []string{"off", "low", "high"}}
	cases := []struct {
		name, effort string
		model        *commandcodeauth.CatalogModel
		want         string
	}{
		{"listed", "high", model, "high"},
		{"disabled", "none", model, "off"},
		{"unlisted", "xhigh", model, ""},
		{"unnamed", "", model, ""},
		{"model without efforts", "high", &commandcodeauth.CatalogModel{ID: "m"}, ""},
		{"disable unsupported", "none", &commandcodeauth.CatalogModel{ID: "m", ReasoningEfforts: []string{"low"}}, ""},
		{"unknown model", "high", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"input":"hi"`
			if tc.effort != "" {
				body += `,"reasoning":{"effort":"` + tc.effort + `"}`
			}
			got := buildCommandCodeTest(t, body+`}`, tc.model, "")
			value := got.Get("params.reasoning_effort")
			if tc.want == "" && value.Exists() || tc.want != "" && value.String() != tc.want {
				t.Errorf("reasoning_effort = %s, want %q", value.Raw, tc.want)
			}
		})
	}
}

func TestCommandCodeRequestKeepsMaxTokensWithinLimits(t *testing.T) {
	capped := &commandcodeauth.CatalogModel{ID: "m", MaxOutputTokens: 8192}
	cases := []struct {
		name  string
		body  string
		model *commandcodeauth.CatalogModel
		want  int64
	}{
		{"client limit", `{"input":"hi","max_output_tokens":1000}`, nil, 1000},
		{"model cap", `{"input":"hi","max_output_tokens":100000}`, capped, 8192},
		{"default under cap", `{"input":"hi"}`, capped, 8192},
		{"gateway ceiling", `{"input":"hi","max_output_tokens":900000}`, nil, 200000},
	}
	for _, tc := range cases {
		if got := buildCommandCodeTest(t, tc.body, tc.model, "").Get("params.max_tokens").Int(); got != tc.want {
			t.Errorf("%s: max_tokens = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCommandCodeRequestDeclaresFunctionToolsOnly(t *testing.T) {
	got := buildCommandCodeTest(t, `{"input":"hi","tools":[
		{"type":"function","name":"ls","description":"List","parameters":{"type":"object","properties":{"p":{"type":"string"}}}},
		{"type":"function","name":"noargs"},
		{"type":"web_search"},{"type":"custom","name":"freeform"}]}`, nil, "")
	tools := got.Get("params.tools").Array()
	if len(tools) != 2 || tools[0].Get("name").String() != "ls" || tools[0].Get("input_schema.properties.p.type").String() != "string" ||
		tools[1].Get("input_schema.type").String() != "object" || tools[0].Get("parameters").Exists() {
		t.Errorf("tools = %s", got.Get("params.tools").Raw)
	}
}

func TestCommandCodeThreadIDFollowsTheSession(t *testing.T) {
	a := buildCommandCodeTest(t, `{"input":"hi"}`, nil, "session-1").Get("threadId").String()
	b := buildCommandCodeTest(t, `{"input":"again"}`, nil, "session-1").Get("threadId").String()
	c := buildCommandCodeTest(t, `{"input":"hi"}`, nil, "session-2").Get("threadId").String()
	d := buildCommandCodeTest(t, `{"input":"hi"}`, nil, "").Get("threadId").String()
	e := buildCommandCodeTest(t, `{"input":"hi"}`, nil, "").Get("threadId").String()
	if a != b || a == c || d == e || len(a) != 36 {
		t.Errorf("thread ids = %q %q %q %q %q", a, b, c, d, e)
	}
}

func TestCommandCodeRequestRejectsEmptyInput(t *testing.T) {
	_, err := buildCommandCodeRequest(commandCodeRequestInput{Body: []byte(`{"input":[]}`), Model: "m"})
	var status statusErr
	if !errors.As(err, &status) || status.code != http.StatusBadRequest {
		t.Errorf("error = %v", err)
	}
}

func TestCommandCodeRequestedMaxTokensReadsEveryClientFormat(t *testing.T) {
	cases := map[string]int64{
		`{"max_output_tokens":10}`:                                10,
		`{"max_completion_tokens":20,"max_tokens":30}`:            20,
		`{"max_tokens":30}`:                                       30,
		`{"generationConfig":{"maxOutputTokens":40}}`:             40,
		`{"request":{"generationConfig":{"maxOutputTokens":50}}}`: 50,
		`{"messages":[]}`:                                         0,
	}
	for payload, want := range cases {
		if got := commandCodeRequestedMaxTokens([]byte(payload)); got != want {
			t.Errorf("%s: %d, want %d", payload, got, want)
		}
	}
}
