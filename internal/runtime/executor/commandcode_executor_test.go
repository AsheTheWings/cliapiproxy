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

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const commandCodeTestCatalog = `{"catalogVersion":"v1","data":[
  {"id":"deepseek/deepseek-v4-flash","name":"Flash","category":"opensource","minimumPlan":"individual-go","context_length":1000000,"maxOutputTokens":16000,
   "inputModalities":["text","image"],"reasoningEfforts":["off","low","high"]},
  {"id":"plain/model","name":"Plain","category":"opensource","inputModalities":["text"],"reasoningEfforts":null}]}`

func commandCodeTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	catalog, err := commandcodeauth.ParseCatalog([]byte(commandCodeTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	catalog.Account = commandcodeauth.Account{UserID: "u1", PlanID: "individual-go"}
	stored, err := catalog.Metadata(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &cliproxyauth.Auth{
		ID:       "commandcode-test.json",
		Provider: "commandcode",
		Attributes: map[string]string{
			cliproxyauth.AttributeSource:         "config:commandcode[test]",
			cliproxyauth.AttributeAPIKey:         "key-1",
			cliproxyauth.AttributeAccountCatalog: "true",
		},
		Metadata: map[string]any{
			"type":                             "commandcode",
			"user_id":                          "u1",
			commandcodeauth.CatalogMetadataKey: stored,
		},
	}
}

type commandCodeTestServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []commandCodeRecordedRequest
}

type commandCodeRecordedRequest struct {
	path   string
	header http.Header
	body   string
}

func (s *commandCodeTestServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *commandCodeTestServer) last(t *testing.T) commandCodeRecordedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.requests[len(s.requests)-1]
}

func newCommandCodeTestServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*commandCodeTestServer, *CommandCodeExecutor) {
	t.Helper()
	server := &commandCodeTestServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		server.mu.Lock()
		server.requests = append(server.requests, commandCodeRecordedRequest{path: r.URL.Path, header: r.Header.Clone(), body: string(body)})
		server.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	executor := NewCommandCodeExecutor(&config.Config{})
	executor.generateURL = server.URL + "/alpha/generate"
	executor.apiURL = server.URL
	return server, executor
}

func writeCommandCodeLines(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, line := range lines {
		_, _ = w.Write([]byte(line + "\n"))
	}
}

var commandCodeTextTurn = []string{
	`{"type":"start"}`,
	`{"type":"start-step"}`,
	`{"type":"reasoning-start","id":"0"}`,
	`{"type":"reasoning-delta","id":"0","text":"think"}`,
	`{"type":"reasoning-end","id":"0"}`,
	`{"type":"text-delta","id":"1","text":"pong"}`,
	`{"type":"finish-step"}`,
	`{"type":"finish","finishReason":"stop","rawFinishReason":"stop","totalUsage":{"inputTokens":40,"inputTokenDetails":{"cacheReadTokens":10},"outputTokens":7,"outputTokenDetails":{"reasoningTokens":3},"totalTokens":47}}`,
}

const commandCodeChatRequest = `{"model":"commandcode/deepseek/deepseek-v4-flash","messages":[
  {"role":"system","content":"You are terse."},
  {"role":"user","content":"ping"}],
  "temperature":0.3,"max_tokens":50,"tool_choice":"auto",
  "tools":[{"type":"function","function":{"name":"read","description":"Read a path","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`

func commandCodeChatOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}
}

func commandCodeChatRequestFor(model string) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`),
	}
}

func TestCommandCodeExecutorSendsTheCLIRequest(t *testing.T) {
	server, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, commandCodeTextTurn...)
	})
	resp, err := executor.Execute(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
	}, commandCodeChatOptions())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := server.last(t)
	if got.path != "/alpha/generate" {
		t.Errorf("path = %s", got.path)
	}
	wantHeaders := map[string]string{
		"Authorization":          "Bearer key-1",
		"Content-Type":           "application/json",
		"User-Agent":             commandcodeauth.UserAgent,
		"X-Command-Code-Version": commandcodeauth.CLIVersion,
		"X-Cli-Environment":      commandcodeauth.CLIEnvironment,
		"X-Taste-Learning":       "false",
		"X-Project-Slug":         "work",
	}
	for name, want := range wantHeaders {
		if have := got.header.Get(name); have != want {
			t.Errorf("header %s = %q, want %q", name, have, want)
		}
	}
	request := gjson.Parse(got.body)
	if got.header.Get("x-session-id") == "" || got.header.Get("x-session-id") != request.Get("threadId").String() {
		t.Errorf("x-session-id %q must equal threadId %q", got.header.Get("x-session-id"), request.Get("threadId").String())
	}
	params := request.Get("params")
	if params.Get("model").String() != "deepseek/deepseek-v4-flash" || !params.Get("stream").Bool() ||
		params.Get("max_tokens").Int() != 50 || params.Get("temperature").Exists() || params.Get("tool_choice").Exists() ||
		params.Get("reasoning_effort").Exists() {
		t.Errorf("params = %s", params.Raw)
	}
	if params.Get("system.0.text").String() != "You are terse." || params.Get("messages.0.content.0.text").String() != "ping" ||
		params.Get("tools.0.name").String() != "read" {
		t.Errorf("params = %s", params.Raw)
	}

	if text := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); text != "pong" {
		t.Errorf("answer = %q in %s", text, resp.Payload)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String() != "think" {
		t.Errorf("the reasoning was lost: %s", resp.Payload)
	}
	usage := gjson.GetBytes(resp.Payload, "usage")
	if usage.Get("prompt_tokens").Int() != 40 || usage.Get("completion_tokens").Int() != 7 ||
		usage.Get("prompt_tokens_details.cached_tokens").Int() != 10 || usage.Get("completion_tokens_details.reasoning_tokens").Int() != 3 {
		t.Errorf("usage = %s", usage.Raw)
	}
}

func TestCommandCodeExecutorStreamsAToolCall(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w,
			`{"type":"start"}`,
			`{"type":"tool-call","toolCallId":"call_1","toolName":"read","input":{"path":"."}}`,
			`{"type":"finish","finishReason":"tool-calls","rawFinishReason":"tool_calls","totalUsage":{"inputTokens":3,"outputTokens":4}}`)
	})
	opts := commandCodeChatOptions()
	opts.Stream = true
	result, err := executor.ExecuteStream(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
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

func TestCommandCodeExecutorReplaysAResponsesConversation(t *testing.T) {
	server, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, commandCodeTextTurn...)
	})
	request := `{"model":"commandcode/deepseek/deepseek-v4-flash","stream":true,"instructions":"be terse","store":false,
	  "include":["reasoning.encrypted_content"],"parallel_tool_calls":true,
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
	    {"type":"reasoning","summary":[],"encrypted_content":"{\"commandcode_reasoning\":\"earlier thought\"}"},
	    {"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},
	    {"type":"function_call_output","call_id":"call_1","output":"ok"}],
	  "tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}]}`
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	if _, err := executor.Execute(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(request),
	}, opts); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	messages := gjson.Parse(server.last(t).body).Get("params.messages")
	var shape []string
	for _, message := range messages.Array() {
		shape = append(shape, message.Get("role").String())
	}
	if strings.Join(shape, ",") != "user,assistant,tool" {
		t.Fatalf("messages = %s", messages.Raw)
	}
	if messages.Get("1.content.0.type").String() != "reasoning" || messages.Get("1.content.0.text").String() != "earlier thought" ||
		messages.Get("1.content.1.toolCallId").String() != "call_1" || messages.Get("2.content.0.output.value").String() != "ok" {
		t.Errorf("messages = %s", messages.Raw)
	}
}

func TestCommandCodeExecutorPassesFailuresThrough(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"success":false,"error":{"code":"UNAUTHORIZED","status":401,"message":"Invalid token"}}`},
		{http.StatusForbidden, `{"success":false,"error":{"code":"FORBIDDEN","status":403,"message":"MODEL_NOT_IN_PLAN: upgrade"}}`},
		{http.StatusForbidden, `{"error":{"code":"upgrade_required","message":"update the CLI","minVersion":"0.18.10"}}`},
		{http.StatusBadRequest, `{"success":false,"error":{"code":"BAD_REQUEST","status":400,"message":"bad"}}`},
		{http.StatusTooManyRequests, `{"error":{"message":"busy"}}`},
	}
	for _, tc := range cases {
		_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := executor.Execute(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
			Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
		}, commandCodeChatOptions())
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != tc.status || err.Error() != tc.body {
			t.Errorf("status %d: error = %v", tc.status, err)
		}
	}
}

func TestCommandCodeExecutorReportsAnErrorEventOfAnAcceptedRequest(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, `{"type":"start"}`,
			`{"type":"error","error":{"type":"overloaded","message":"upstream busy","statusCode":529,"isRetryable":true}}`)
	})
	_, err := executor.Execute(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
	}, commandCodeChatOptions())
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 529 || !strings.Contains(err.Error(), "upstream busy") {
		t.Fatalf("error = %v, want status 529", err)
	}
}

// The conductor moves a stream to another credential only while no payload has
// reached the client, so an error before any output must come first.
func TestCommandCodeExecutorStreamsAnErrorBeforeOutputAsTheFirstChunk(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, `{"type":"start"}`, `{"type":"start-step"}`,
			`{"type":"error","error":{"message":"rate limited","statusCode":429,"isRetryable":true}}`)
	})
	opts := commandCodeChatOptions()
	opts.Stream = true
	result, err := executor.ExecuteStream(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	first, ok := <-result.Chunks
	for range result.Chunks {
	}
	var status interface{ StatusCode() int }
	if !ok || len(first.Payload) > 0 || !errors.As(first.Err, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("first chunk = %q, %v; want the 429 error alone", first.Payload, first.Err)
	}
}

func TestCommandCodeExecutorReportsAStreamThatEndsWithoutAnOutcome(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, commandCodeTextTurn[:5]...)
	})
	opts := commandCodeChatOptions()
	opts.Stream = true
	result, err := executor.ExecuteStream(context.Background(), commandCodeTestAuth(t), cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
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
}

func TestCommandCodeExecutorRefusesACredentialWithoutAKey(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request left without a key")
	})
	auth := commandCodeTestAuth(t)
	delete(auth.Attributes, cliproxyauth.AttributeAPIKey)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "commandcode/deepseek/deepseek-v4-flash", Payload: []byte(commandCodeChatRequest),
	}, commandCodeChatOptions())
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
}

func TestCommandCodeModelsApplyThinkingLevels(t *testing.T) {
	catalog, err := commandcodeauth.ParseCatalog([]byte(commandCodeTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient("commandcode-thinking-test", "commandcode", catalog.ModelInfos())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("commandcode-thinking-test") })

	server, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCommandCodeLines(w, commandCodeTextTurn...)
	})
	effort := func(model string) string {
		t.Helper()
		if _, err := executor.Execute(context.Background(), commandCodeTestAuth(t), commandCodeChatRequestFor(model), commandCodeChatOptions()); err != nil {
			t.Fatalf("Execute(%s): %v", model, err)
		}
		return gjson.Get(server.last(t).body, "params.reasoning_effort").String()
	}
	for model, want := range map[string]string{
		"commandcode/deepseek/deepseek-v4-flash(low)":  "low",
		"commandcode/deepseek/deepseek-v4-flash(high)": "high",
		"commandcode/deepseek/deepseek-v4-flash(none)": "off",
		"commandcode/deepseek/deepseek-v4-flash":       "",
	} {
		if got := effort(model); got != want {
			t.Errorf("%s: reasoning_effort = %q, want %q", model, got, want)
		}
	}
	// A level the model lacks is refused before any request goes out.
	before := server.count()
	_, err = executor.Execute(context.Background(), commandCodeTestAuth(t), commandCodeChatRequestFor("commandcode/deepseek/deepseek-v4-flash(max)"), commandCodeChatOptions())
	if err == nil || server.count() != before {
		t.Errorf("an unsupported level gave err=%v and %d requests", err, server.count()-before)
	}
	// A model without efforts takes no effort.
	if _, err = executor.Execute(context.Background(), commandCodeTestAuth(t), commandCodeChatRequestFor("commandcode/plain/model"), commandCodeChatOptions()); err != nil {
		t.Fatal(err)
	}
	if gjson.Get(server.last(t).body, "params.reasoning_effort").Exists() {
		t.Errorf("a model without efforts received one: %s", server.last(t).body)
	}
}

func TestCommandCodeExecutorRefreshReloadsPlanAndCatalog(t *testing.T) {
	server, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			_, _ = w.Write([]byte(`{"user":{"id":"u1","userName":"ada"},"org":null}`))
		case "/alpha/billing/subscriptions":
			_, _ = w.Write([]byte(`{"success":true,"data":{"planId":"individual-pro","status":"active"}}`))
		case "/alpha/billing/credits":
			_, _ = w.Write([]byte(`{"credits":{"purchasedCredits":0,"freeCredits":0}}`))
		case "/provider/v1/models":
			_, _ = w.Write([]byte(`{"catalogVersion":"v2","data":[{"id":"new/model","category":"premium","minimumPlan":"individual-pro"}]}`))
		}
	})
	auth := commandCodeTestAuth(t)
	refreshed, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if server.count() != 4 || server.last(t).header.Get("Authorization") != "Bearer key-1" {
		t.Errorf("requests = %d", server.count())
	}
	catalog, ok := commandcodeauth.CatalogFromMetadata(refreshed.Metadata)
	if !ok || catalog.Version != "v2" || catalog.Account.PlanID != "individual-pro" || len(catalog.Available()) != 1 {
		t.Fatalf("catalog = %+v", catalog)
	}
	expired, err := time.Parse(time.RFC3339, refreshed.Metadata["expired"].(string))
	if err != nil || time.Until(expired) < 50*time.Minute {
		t.Errorf("expired = %v (%v)", refreshed.Metadata["expired"], err)
	}
}

func TestCommandCodeExecutorRefreshKeepsTheCatalogWhenItCannotBeReloaded(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			_, _ = w.Write([]byte(`{"user":{"id":"u1"},"org":null}`))
		case "/provider/v1/models":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	auth := commandCodeTestAuth(t)
	refreshed, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	catalog, ok := commandcodeauth.CatalogFromMetadata(refreshed.Metadata)
	if !ok || catalog.Version != "v1" || len(catalog.Models) != 2 {
		t.Errorf("the previous catalog was lost: %+v", catalog)
	}

	// Without a previous catalog the refresh fails, so it is retried.
	delete(auth.Metadata, commandcodeauth.CatalogMetadataKey)
	if _, err = executor.Refresh(context.Background(), auth); err == nil {
		t.Error("a first catalog load that failed counted as a refresh")
	}
}

func TestCommandCodeExecutorRefreshReportsARevokedKey(t *testing.T) {
	_, executor := newCommandCodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","status":401,"message":"Invalid token"}}`))
	})
	_, err := executor.Refresh(context.Background(), commandCodeTestAuth(t))
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
}
