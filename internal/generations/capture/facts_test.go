//go:build unit

package capture

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestTerminalSignalsAndInterruptedStreams(t *testing.T) {
	for _, body := range []string{
		`data: [DONE]`,
		`data: {"type":"response.completed"}`,
		`data: {"type":"response.incomplete"}`,
		`{"object":"response","status":"completed"}`,
		`data: {"type":"image_generation.completed"}`,
		`data: {"type":"message_stop"}`,
		`data: {"choices":[{"finish_reason":"stop"}]}`,
		`data: {"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		`data: {"response":{"candidates":[{"finishReason":"STOP"}]}}`,
		`{"type":"finish","finishReason":"length"}`,
		`{"event_type":"interaction.completed"}`,
	} {
		var f responseFacts
		f.observe([]byte(body))
		if !f.terminal {
			t.Errorf("terminal signal not recognized: %s", body)
		}
	}
	for _, body := range []string{
		`data: {"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
		`data: {"type":"response.completed"`,
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`data: {"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`,
		`data: {"choices":[{"finish_reason":false}]}`,
	} {
		var f responseFacts
		f.observe([]byte(body))
		if f.terminal {
			t.Errorf("partial response recognized as terminal: %s", body)
		}
	}
}

func TestTransportFactsDoNotDependOnAnyByteBoundary(t *testing.T) {
	const response = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9}}}\r\n\r\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"output_tokens\":7,\"kiro:credits\":{\"value\":0.2}}}}\r\n\r\n"
	for split := 0; split <= len(response); split++ {
		var f responseFacts
		f.fragment([]byte(response[:split]))
		f.fragment([]byte(response[split:]))
		f.finish()
		raw, amount, unit := f.values(false)
		if !f.terminal || !f.incomplete || amount != "0.2" || unit != "credits" ||
			gjson.GetBytes(raw, "input_tokens").Int() != 9 || gjson.GetBytes(raw, "output_tokens").Int() != 7 {
			t.Fatalf("split at byte %d lost terminal or usage facts", split)
		}
	}
	var f responseFacts
	for _, b := range []byte(response) {
		f.fragment([]byte{b})
	}
	f.finish()
	if !f.terminal || !f.incomplete {
		t.Fatal("one-byte transport chunks lost their terminal signal")
	}
}

func TestSSETerminalNameRequiresACompleteDataDocument(t *testing.T) {
	var f responseFacts
	f.fragment([]byte("event: response.incomplete\r\ndata: {\"usage\":{\"output_tokens\":7}}\r\n\r\n"))
	if !f.terminal || !f.incomplete {
		t.Fatal("the SSE event name was lost")
	}
	var malformed responseFacts
	malformed.fragment([]byte("event: response.completed\ndata: {unfinished\n\n"))
	malformed.finish()
	if malformed.terminal {
		t.Fatal("a header with malformed event data completed a generation")
	}
}

func TestPrettyJSONTransportFactsSurviveEveryByteBoundary(t *testing.T) {
	const body = "{\n\"object\":\"response\",\n\"status\":\"incomplete\",\n\"usage\":{\"kiro:credits\":{\"value\":0.2}}\n}"
	var f responseFacts
	for _, b := range []byte(body) {
		f.fragment([]byte{b})
	}
	f.finish()
	_, amount, unit := f.values(false)
	if !f.terminal || !f.incomplete || amount != "0.2" || unit != "credits" {
		t.Fatal("pretty-printed JSON facts were lost across transport boundaries")
	}
}

func TestFactsReadMultilineEventsAndDoNotCarryMalformedFrames(t *testing.T) {
	var f responseFacts
	for _, line := range []string{`data: {broken`, "", `data: {"type":`, `data: "response.incomplete","response":{"usage":{"kiro:credits":{"value":0.2}}}}`, ""} {
		f.observe([]byte(line))
	}
	_, amount, unit := f.values(false)
	if !f.terminal || !f.incomplete || amount != "0.2" || unit != "credits" {
		t.Fatalf("facts = terminal %v, incomplete %v, amount %q %q", f.terminal, f.incomplete, amount, unit)
	}
}
