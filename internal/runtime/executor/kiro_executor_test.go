//go:build component

package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const kiroTestCatalog = `{"defaultModel":{"modelId":"auto"},"models":[
  {"modelId":"claude-haiku-5.5","supportedInputTypes":["TEXT","IMAGE"],"tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":64000},
   "additionalModelRequestFieldsSchema":{"properties":{
     "thinking":{"properties":{"type":{"enum":["adaptive","disabled"]}}},
     "output_config":{"properties":{"effort":{"enum":["low","medium","high"],"default":"medium"}}}}}},
  {"modelId":"kimi-k3","supportedInputTypes":["TEXT"],"tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":64000},
   "additionalModelRequestFieldsSchema":{"properties":{"reasoning":{"properties":{"effort":{"enum":["low","high","max"],"default":"max"}}}}}},
  {"modelId":"deepseek-3.2","supportedInputTypes":["TEXT"],"tokenLimits":{"maxInputTokens":164000,"maxOutputTokens":64000}}]}`

func kiroTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	catalog, err := kiroauth.ParseCatalog([]byte(kiroTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := catalog.Metadata(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &cliproxyauth.Auth{
		ID:       "kiro-test.json",
		Provider: "kiro",
		Metadata: map[string]any{
			"type":                      "kiro",
			"access_token":              "access-1",
			"refresh_token":             "refresh-1",
			"profile_arn":               "arn:aws:codewhisperer:us-east-1:111:profile/TEST",
			kiroauth.CatalogMetadataKey: stored,
		},
	}
}

// kiroTestServer answers inference requests with the given handler and records
// the requests it received.
type kiroTestServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []kiroRecordedRequest
}

type kiroRecordedRequest struct {
	header http.Header
	body   string
}

func (s *kiroTestServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *kiroTestServer) last(t *testing.T) kiroRecordedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.requests[len(s.requests)-1]
}

func newKiroTestServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*kiroTestServer, *KiroExecutor) {
	t.Helper()
	server := &kiroTestServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		server.mu.Lock()
		server.requests = append(server.requests, kiroRecordedRequest{header: r.Header.Clone(), body: string(body)})
		server.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	executor := NewKiroExecutor(&config.Config{})
	executor.inferenceURL = server.URL + "/v1/responses"
	return server, executor
}

func writeKiroEvents(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		name := gjson.Get(event, "type").String()
		_, _ = w.Write([]byte("event: " + name + "\ndata: " + event + "\n\n"))
	}
}

const kiroTextTurn = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"claude-haiku-5.5","output":[]}}
{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}
{"type":"response.content_part.added","sequence_number":2,"output_index":0,"item_id":"msg_1","content_index":0,"part":{"type":"output_text","text":""}}
{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"item_id":"msg_1","content_index":0,"delta":"pong"}
{"type":"response.output_text.done","sequence_number":4,"output_index":0,"item_id":"msg_1","content_index":0,"text":"pong"}
{"type":"response.content_part.done","sequence_number":5,"output_index":0,"item_id":"msg_1","content_index":0,"part":{"type":"output_text","text":"pong"}}
{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong"}]}}
{"type":"response.completed","sequence_number":7,"response":{"id":"resp_1","object":"response","status":"completed","model":"claude-haiku-5.5","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong"}]}],"usage":{"kiro:credits":{"value":0.0283,"unit":"credit","unit_plural":"credits"},"kiro:context_usage_percentage":0.5}}}`

const kiroToolTurn = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","status":"in_progress","model":"claude-haiku-5.5","output":[]}}
{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"toolu_1","name":"read","arguments":"","status":"in_progress"}}
{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"path\": \".\"}"}
{"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"fc_1","arguments":"{\"path\": \".\"}"}
{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"toolu_1","name":"read","arguments":"{\"path\": \".\"}","status":"completed"}}
{"type":"response.completed","sequence_number":5,"response":{"id":"resp_2","object":"response","status":"requires_action","model":"claude-haiku-5.5","output":[{"id":"fc_1","type":"function_call","call_id":"toolu_1","name":"read","arguments":"{\"path\": \".\"}","status":"completed"}],"usage":{"kiro:credits":{"value":0.0036},"kiro:context_usage_percentage":2.5}}}`

func kiroEvents(stream string) []string {
	return strings.Split(strings.TrimSpace(stream), "\n")
}

const kiroChatRequest = `{"model":"kiro/claude-haiku-5.5","messages":[
  {"role":"system","content":"You are terse."},
  {"role":"user","content":"ping"}],
  "temperature":0.3,"max_tokens":50,"tool_choice":"auto",
  "tools":[{"type":"function","function":{"name":"read","description":"Read a path","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`

func kiroChatOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}
}

func TestKiroExecutorSendsTheAgentRequest(t *testing.T) {
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	resp, err := executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, kiroChatOptions())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := server.last(t)
	wantHeaders := map[string]string{
		"Authorization":               "Bearer access-1",
		"Content-Type":                "application/json",
		"X-Amzn-Kiro-Origin":          "AI_EDITOR",
		"X-Amzn-Codewhisperer-Optout": "true",
		"X-Amzn-Kiro-Profile":         "arn:aws:codewhisperer:us-east-1:111:profile/TEST",
		"User-Agent":                  kiroauth.InferenceUserAgent,
		"X-Kiro-Attempt":              "1;max=3",
	}
	for name, want := range wantHeaders {
		if have := got.header.Get(name); have != want {
			t.Errorf("header %s = %q, want %q", name, have, want)
		}
	}
	body := gjson.Parse(got.body)
	if body.Get("model").String() != "claude-haiku-5.5" || !body.Get("stream").Bool() {
		t.Errorf("body = %s", got.body)
	}
	for _, field := range []string{"temperature", "max_tokens", "max_output_tokens", "tool_choice", "instructions", "store", "include"} {
		if body.Get(field).Exists() {
			t.Errorf("body carries %s: %s", field, got.body)
		}
	}
	if body.Get("input.0.content").String() != "You are terse." || body.Get("input.1.role").String() != "assistant" ||
		body.Get("input.2.content").String() != "ping" {
		t.Errorf("input = %s", body.Get("input").Raw)
	}
	if body.Get("tools.0.name").String() != "read" {
		t.Errorf("tools = %s", body.Get("tools").Raw)
	}
	if body.Get("reasoning").Raw != `{"type":"adaptive","effort":"medium"}` {
		t.Errorf("reasoning = %s", body.Get("reasoning").Raw)
	}
	if !strings.HasPrefix(body.Get("metadata.kiro:conversation_id").String(), "sess_") {
		t.Errorf("metadata = %s", body.Get("metadata").Raw)
	}

	if text := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); text != "pong" {
		t.Errorf("answer = %q in %s", text, resp.Payload)
	}
	if tokens := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int(); tokens != 5000 {
		t.Errorf("prompt tokens = %d, want 5000 (0.5%% of the window): %s", tokens, resp.Payload)
	}
}

func TestKiroExecutorStreamsAToolCall(t *testing.T) {
	_, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroToolTurn)...)
	})
	opts := kiroChatOptions()
	opts.Stream = true
	result, err := executor.ExecuteStream(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var stream strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}
	out := stream.String()
	if !strings.Contains(out, `"name":"read"`) || !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Errorf("the tool call did not reach the client:\n%s", out)
	}
}

func TestKiroExecutorReplaysAResponsesConversation(t *testing.T) {
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	signed := `{"reasoningText":{"text":"t","signature":"sig"}}`
	request := `{"model":"kiro/claude-haiku-5.5","stream":true,"instructions":"be terse","store":false,
	  "include":["reasoning.encrypted_content"],"reasoning":{"effort":"high","summary":"auto"},"parallel_tool_calls":true,
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
	    {"type":"reasoning","summary":[],"encrypted_content":` + string(kiroMarshal(signed)) + `},
	    {"type":"function_call","call_id":"toolu_1","name":"read","arguments":"{}"},
	    {"type":"function_call_output","call_id":"toolu_1","output":"ok"}],
	  "tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}]}`
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	_, err := executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(request),
	}, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body := gjson.Parse(server.last(t).body)
	var shape []string
	for _, item := range body.Get("input").Array() {
		kind := item.Get("type").String()
		if kind == "" {
			kind = item.Get("role").String()
		}
		shape = append(shape, kind)
	}
	if want := "user,assistant,user,reasoning,function_call,function_call_output"; strings.Join(shape, ",") != want {
		t.Errorf("input shape = %v, want %s", shape, want)
	}
	if body.Get("input.3.encrypted_content").String() != signed {
		t.Errorf("the reasoning item changed: %s", body.Get("input.3").Raw)
	}
	if body.Get("reasoning").Raw != `{"type":"adaptive","effort":"high"}` {
		t.Errorf("reasoning = %s", body.Get("reasoning").Raw)
	}
}

func TestKiroExecutorReportsARejectedTokenAsUnauthorized(t *testing.T) {
	_, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"The bearer token included in the request is invalid.","type":"access_denied","param":null,"code":"access_denied"}}`))
	})
	_, err := executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, kiroChatOptions())
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
}

func TestKiroExecutorPassesOtherFailuresThrough(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusForbidden, `{"error":{"message":"This model is not available on your plan.","type":"access_denied","code":"access_denied"}}`},
		{http.StatusRequestEntityTooLarge, `{"error":{"message":"Input content length exceeds threshold.","type":"invalid_request","code":"context_length_exceeded"}}`},
		{http.StatusBadRequest, `{"error":{"message":"bad","type":"invalid_request","code":"unsupported_parameter"}}`},
	}
	for _, tc := range cases {
		_, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
			Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
		}, kiroChatOptions())
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != tc.status || err.Error() != tc.body {
			t.Errorf("status %d: error = %v", tc.status, err)
		}
	}
}

func TestKiroExecutorRefusesACredentialWithoutAProfile(t *testing.T) {
	_, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request left without a profile")
	})
	auth := kiroTestAuth(t)
	delete(auth.Metadata, "profile_arn")
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, kiroChatOptions())
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
}

func TestKiroModelsApplyThinkingLevels(t *testing.T) {
	catalog, err := kiroauth.ParseCatalog([]byte(kiroTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient("kiro-thinking-test", "kiro", catalog.ModelInfos())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("kiro-thinking-test") })

	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	for suffix, want := range map[string]string{"low": `{"type":"adaptive","effort":"low"}`, "none": `{"type":"disabled"}`} {
		model := "kiro/claude-haiku-5.5(" + suffix + ")"
		_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`),
		}, kiroChatOptions())
		if err != nil {
			t.Fatalf("Execute(%s): %v", suffix, err)
		}
		if got := gjson.Get(server.last(t).body, "reasoning").Raw; got != want {
			t.Errorf("suffix %s: reasoning = %s, want %s", suffix, got, want)
		}
	}
	// A level the model lacks is refused before any request goes out.
	before := server.count()
	_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model:   "kiro/claude-haiku-5.5(max)",
		Payload: []byte(`{"model":"kiro/claude-haiku-5.5(max)","messages":[{"role":"user","content":"hi"}]}`),
	}, kiroChatOptions())
	if err == nil || server.count() != before {
		t.Errorf("an unsupported level gave err=%v and %d requests", err, server.count()-before)
	}
}

func TestKiroExecutorRefreshStoresTheTokenAndCatalog(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/refreshToken":
			_, _ = w.Write([]byte(`{"accessToken":"access-2","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:111:profile/TEST","refreshToken":"refresh-1"}`))
		default:
			if r.Header.Get("Authorization") != "Bearer access-2" {
				t.Errorf("catalog requested with %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"models":[{"modelId":"deepseek-3.2","tokenLimits":{"maxInputTokens":164000}}]}`))
		}
	}))
	defer server.Close()

	executor := NewKiroExecutor(&config.Config{})
	executor.authURL, executor.managementURL = server.URL, server.URL
	auth := kiroTestAuth(t)
	refreshed, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.Metadata["access_token"] != "access-2" || refreshed.Metadata["refresh_token"] != "refresh-1" {
		t.Errorf("tokens = %v", refreshed.Metadata)
	}
	expired, err := time.Parse(time.RFC3339, refreshed.Metadata["expired"].(string))
	if err != nil || time.Until(expired) < 59*time.Minute {
		t.Errorf("expired = %v (%v)", refreshed.Metadata["expired"], err)
	}
	catalog, ok := kiroauth.CatalogFromMetadata(refreshed.Metadata)
	if !ok || len(catalog.Models) != 1 || catalog.Models[0].ModelID != "deepseek-3.2" {
		t.Errorf("catalog = %+v", catalog)
	}

	// A catalog that cannot be loaded keeps the previous one.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refreshToken" {
			_, _ = w.Write([]byte(`{"accessToken":"access-3","expiresIn":3600,"profileArn":"arn:p","refreshToken":"refresh-1"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	again, err := executor.Refresh(context.Background(), refreshed)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if catalog, ok = kiroauth.CatalogFromMetadata(again.Metadata); !ok || catalog.Models[0].ModelID != "deepseek-3.2" {
		t.Errorf("catalog after a failed load = %+v", catalog)
	}
	if again.Metadata["access_token"] != "access-3" {
		t.Errorf("access token = %v", again.Metadata["access_token"])
	}
}

func TestKiroExecutorRefreshReportsABadRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()
	executor := NewKiroExecutor(&config.Config{})
	executor.authURL, executor.managementURL = server.URL, server.URL
	_, err := executor.Refresh(context.Background(), kiroTestAuth(t))
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
}

func TestKiroExecutorOmitsReasoningThatOtherAPIsCarry(t *testing.T) {
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	signed := string(kiroMarshal(`{"reasoningText":{"text":"t","signature":"sig"}}`))
	request := `{"model":"kiro/claude-haiku-5.5","max_tokens":100,"messages":[
	  {"role":"user","content":"q"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":` + signed + `},{"type":"text","text":"a"}]},
	  {"role":"user","content":"next"}]}`
	_, err := executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(request),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, item := range gjson.Get(server.last(t).body, "input").Array() {
		if item.Get("type").String() == "reasoning" {
			t.Errorf("a reasoning item reached the service: %s", item.Raw)
		}
	}
}

func TestKiroExecutorRefusesAnEffortTheModelLacks(t *testing.T) {
	catalog, err := kiroauth.ParseCatalog([]byte(kiroTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient("kiro-effort-test", "kiro", catalog.ModelInfos())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("kiro-effort-test") })
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model:   "kiro/claude-haiku-5.5",
		Payload: []byte(`{"model":"kiro/claude-haiku-5.5","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`),
	}, kiroChatOptions())
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest || server.count() != 0 {
		t.Errorf("an effort outside the model's levels gave err=%v and %d requests", err, server.count())
	}
}

func TestKiroExecutorUsesTheModelDefaultEffortWhenTheClientNamesNone(t *testing.T) {
	catalog, err := kiroauth.ParseCatalog([]byte(kiroTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient("kiro-default-effort-test", "kiro", catalog.ModelInfos())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("kiro-default-effort-test") })
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	for _, tc := range []struct{ source, field, want string }{
		{"openai", ``, "max"},
		{"openai", `"reasoning_effort":"low",`, "low"},
		{"openai-response", ``, "max"},
	} {
		payload := `{"model":"kiro/kimi-k3",` + tc.field + `"messages":[{"role":"user","content":"hi"}]}`
		if tc.source == "openai-response" {
			payload = `{"model":"kiro/kimi-k3","input":"hi"}`
		}
		_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
			Model: "kiro/kimi-k3", Payload: []byte(payload),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(tc.source)})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.source, tc.field, err)
		}
		if got := gjson.Get(server.last(t).body, "reasoning.effort").String(); got != tc.want {
			t.Errorf("%s %s: effort = %q, want %q", tc.source, tc.field, got, tc.want)
		}
	}
}

func TestKiroExecutorReportsAStreamThatEndsWithoutAnOutcome(t *testing.T) {
	truncated := kiroEvents(kiroTextTurn)[:4]
	_, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, truncated...)
	})
	opts := kiroChatOptions()
	opts.Stream = true
	result, err := executor.ExecuteStream(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	var status interface{ StatusCode() int }
	if !errors.As(streamErr, &status) || status.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("stream error = %v, want status 408", streamErr)
	}

	_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
		Model: "kiro/claude-haiku-5.5", Payload: []byte(kiroChatRequest),
	}, kiroChatOptions())
	if !errors.As(err, &status) || status.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("Execute error = %v, want status 408", err)
	}
}

func TestKiroExecutorMapsEffortsOfLevelOnlyModels(t *testing.T) {
	catalog, err := kiroauth.ParseCatalog([]byte(kiroTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient("kiro-level-only-test", "kiro", catalog.ModelInfos())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("kiro-level-only-test") })
	server, executor := newKiroTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeKiroEvents(w, kiroEvents(kiroTextTurn)...)
	})
	for _, tc := range []struct{ name, source, payload, want string }{
		{"off runs at the lowest level", "openai", `{"model":"kiro/kimi-k3","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"translated formats move to the nearest level", "claude", `{"model":"kiro/kimi-k3","max_tokens":10,"output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`, "high"},
	} {
		_, err = executor.Execute(context.Background(), kiroTestAuth(t), cliproxyexecutor.Request{
			Model: "kiro/kimi-k3", Payload: []byte(tc.payload),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(tc.source)})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := gjson.Get(server.last(t).body, "reasoning.effort").String(); got != tc.want {
			t.Errorf("%s: effort = %q, want %q", tc.name, got, tc.want)
		}
	}
}
