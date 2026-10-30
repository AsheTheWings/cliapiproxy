//go:build unit

package executor

import (
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type sseEvent struct {
	name string
	data gjson.Result
}

func convertCommandCodeStream(t *testing.T, upstream string) ([]sseEvent, error) {
	t.Helper()
	stream := newCommandCodeStream(io.NopCloser(strings.NewReader(upstream)), "m")
	raw, err := io.ReadAll(stream)
	var events []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		if block == "" {
			continue
		}
		name, data, _ := strings.Cut(block, "\ndata: ")
		event := sseEvent{strings.TrimPrefix(name, "event: "), gjson.Parse(data)}
		if got := event.data.Get("type").String(); got != event.name {
			t.Errorf("event name %q differs from its type %q", event.name, got)
		}
		events = append(events, event)
	}
	return events, err
}

func eventNames(events []sseEvent) string {
	names := make([]string, len(events))
	for i, event := range events {
		names[i] = event.name
	}
	return strings.Join(names, " ")
}

const commandCodeFinish = `{"type":"finish","finishReason":"stop","rawFinishReason":"stop","totalUsage":{"inputTokens":120,"inputTokenDetails":{"cacheReadTokens":100,"cacheWriteTokens":0,"noCacheTokens":20},"outputTokens":30,"outputTokenDetails":{"reasoningTokens":10},"totalTokens":150}}`

func TestCommandCodeStreamConvertsReasoningAndText(t *testing.T) {
	events, err := convertCommandCodeStream(t, `{"type":"start"}
{"type":"start-step"}
{"type":"reasoning-start","id":"0"}
{"type":"reasoning-delta","id":"0","text":"hm"}
{"type":"reasoning-delta","id":"0","text":"m"}
{"type":"reasoning-end","id":"0"}
{"type":"text-start","id":"1"}
{"type":"text-delta","id":"1","text":"Hel"}
{"type":"text-delta","id":"1","text":"lo"}
{"type":"text-end","id":"1"}
{"type":"finish-step"}
`+commandCodeFinish+`
{"type":"provider-metadata"}
`)
	if err != nil {
		t.Fatal(err)
	}
	want := "response.created response.in_progress response.output_item.added response.reasoning_summary_part.added " +
		"response.reasoning_summary_text.delta response.reasoning_summary_text.delta response.reasoning_summary_text.done " +
		"response.reasoning_summary_part.done response.output_item.done response.output_item.added response.content_part.added " +
		"response.output_text.delta response.output_text.delta response.output_text.done response.content_part.done " +
		"response.output_item.done response.completed"
	if got := eventNames(events); got != want {
		t.Fatalf("events:\n%s\nwant:\n%s", got, want)
	}
	for i, event := range events {
		if event.data.Get("sequence_number").Int() != int64(i) {
			t.Errorf("event %d has sequence %d", i, event.data.Get("sequence_number").Int())
		}
	}
	completed := events[len(events)-1].data.Get("response")
	if completed.Get("status").String() != "completed" || completed.Get("output.#").Int() != 2 ||
		completed.Get("output.0.summary.0.text").String() != "hmm" ||
		gjson.Get(completed.Get("output.0.encrypted_content").String(), commandCodeReasoningKey).String() != "hmm" ||
		completed.Get("output.1.content.0.text").String() != "Hello" {
		t.Errorf("response = %s", completed.Raw)
	}
	usage := completed.Get("usage")
	if usage.Get("input_tokens").Int() != 120 || usage.Get("input_tokens_details.cached_tokens").Int() != 100 ||
		usage.Get("output_tokens").Int() != 30 || usage.Get("output_tokens_details.reasoning_tokens").Int() != 10 ||
		usage.Get("total_tokens").Int() != 150 {
		t.Errorf("usage = %s", usage.Raw)
	}
}

func TestCommandCodeStreamConvertsToolCalls(t *testing.T) {
	events, _ := convertCommandCodeStream(t, `{"type":"start"}
{"type":"text-delta","text":"Checking."}
{"type":"tool-input-start","id":"call_1","toolName":"ls"}
{"type":"tool-input-delta","id":"call_1","delta":"{\"p"}
{"type":"tool-input-end","id":"call_1"}
{"type":"tool-call","toolCallId":"call_1","toolName":"ls","input":{"path":"."}}
{"type":"tool-call","toolCallId":"call_2","toolName":"pwd","args":"{\"a\":1}"}
{"type":"finish","finishReason":"tool-calls","rawFinishReason":"tool_calls","totalUsage":{"inputTokens":5,"outputTokens":6}}
`)
	last := events[len(events)-1]
	if last.name != "response.completed" {
		t.Fatalf("terminal event = %s", last.name)
	}
	output := last.data.Get("response.output")
	if output.Get("#").Int() != 3 || output.Get("0.type").String() != "message" {
		t.Fatalf("output = %s", output.Raw)
	}
	call := output.Get("1")
	if call.Get("type").String() != "function_call" || call.Get("call_id").String() != "call_1" ||
		call.Get("name").String() != "ls" || call.Get("arguments").String() != `{"path":"."}` {
		t.Errorf("first call = %s", call.Raw)
	}
	if output.Get("2.arguments").String() != `{"a":1}` {
		t.Errorf("second call = %s", output.Get("2").Raw)
	}
	var deltas int
	for _, event := range events {
		if event.name == "response.function_call_arguments.delta" {
			deltas++
		}
	}
	if deltas != 2 {
		t.Errorf("argument deltas = %d, want one per call", deltas)
	}
}

func TestCommandCodeStreamMapsFinishReasons(t *testing.T) {
	cases := []struct {
		finish, terminal, reason string
	}{
		{`{"type":"finish","finishReason":"length","rawFinishReason":"max_tokens"}`, "response.incomplete", "max_output_tokens"},
		{`{"type":"finish","finishReason":"content-filter","rawFinishReason":"refusal"}`, "response.incomplete", "content_filter"},
		{`{"type":"finish","finishReason":"stop","rawFinishReason":"end_turn"}`, "response.completed", ""},
	}
	for _, tc := range cases {
		events, _ := convertCommandCodeStream(t, `{"type":"text-delta","text":"x"}`+"\n"+tc.finish+"\n")
		last := events[len(events)-1]
		if last.name != tc.terminal || last.data.Get("response.incomplete_details.reason").String() != tc.reason {
			t.Errorf("%s: got %s %s", tc.finish, last.name, last.data.Raw)
		}
	}
}

func TestCommandCodeStreamReportsErrorEvents(t *testing.T) {
	cases := []struct {
		line    string
		code    int64
		message string
	}{
		{`{"type":"error","error":{"type":"overloaded","message":"busy","statusCode":529,"isRetryable":true}}`, 529, "busy"},
		{`{"type":"error","error":"plain failure"}`, 502, "plain failure"},
	}
	for _, tc := range cases {
		events, _ := convertCommandCodeStream(t, `{"type":"start"}`+"\n"+tc.line+"\n"+commandCodeFinish+"\n")
		last := events[len(events)-1]
		if last.name != "error" || last.data.Get("error.code").Int() != tc.code || last.data.Get("error.message").String() != tc.message {
			t.Errorf("%s: got %s", tc.line, last.data.Raw)
		}
		for _, event := range events {
			if event.name == "response.completed" {
				t.Errorf("a stream that failed completed: %s", eventNames(events))
			}
		}
	}
}

func TestCommandCodeStreamWithoutFinishHasNoTerminalEvent(t *testing.T) {
	events, _ := convertCommandCodeStream(t, `{"type":"start"}`+"\n"+`{"type":"text-delta","text":"cut"}`)
	for _, event := range events {
		if event.name == "response.completed" || event.name == "response.incomplete" || event.name == "error" {
			t.Errorf("a truncated stream has the terminal event %s", event.name)
		}
	}
}

func TestCommandCodeToolArgumentsAcceptTheCLIsCoercions(t *testing.T) {
	cases := map[string]string{
		`{"input":{"a":1}}`:     `{"a":1}`,
		`{"input":[{"a":1}]}`:   `{"a":1}`,
		`{"input":"{\"a\":1}"}`: `{"a":1}`,
		`{"args":{"b":2}}`:      `{"b":2}`,
		`{"input":"not json"}`:  `{}`,
		`{}`:                    `{}`,
		`{"input":null}`:        `{}`,
		`{"input":[1,2]}`:       `{}`,
	}
	for event, want := range cases {
		parsed := gjson.Parse(event)
		if got := commandCodeToolArguments(parsed.Get("input"), parsed.Get("args")); got != want {
			t.Errorf("%s: arguments = %s, want %s", event, got, want)
		}
	}
}
