//go:build unit

package capture

import "testing"

func TestSystemInstructionOfEachClientFormat(t *testing.T) {
	for name, tc := range map[string]struct{ input, want string }{
		"responses instructions": {`{"instructions":"be brief","input":"hi"}`, "be brief"},
		"responses developer":    {`{"input":[{"role":"developer","content":[{"type":"input_text","text":"rule"}]},{"role":"user","content":"hi"}]}`, "rule"},
		"chat system":            {`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`, "sys"},
		"claude blocks":          {`{"system":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"messages":[]}`, "a\n\nb"},
		"gemini":                 {`{"systemInstruction":{"parts":[{"text":"g"}]},"contents":[]}`, "g"},
		"none":                   {`{"messages":[{"role":"user","content":"hi"}]}`, ""},
		"not json":               {`hello`, ""},
	} {
		if got := systemInstruction([]byte(tc.input)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestIncompleteStopsOfEachProtocol(t *testing.T) {
	stopped := map[string]string{
		"responses event": "data: {\"type\":\"response.incomplete\"}",
		"responses body":  `{"object":"response","status":"incomplete"}`,
		"claude":          "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}",
		"chat":            `{"choices":[{"finish_reason":"length"}]}`,
		"gemini":          `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		"command code":    "data: {\"type\":\"finish\",\"finishReason\":\"length\"}",
	}
	for name, body := range stopped {
		if !incomplete(documents([]byte(body), false)) {
			t.Errorf("%s: not incomplete", name)
		}
	}
	for name, body := range map[string]string{
		"completed": "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}",
		"stop":      `{"choices":[{"finish_reason":"stop"}]}`,
		"tool use":  "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}",
	} {
		if incomplete(documents([]byte(body), false)) {
			t.Errorf("%s: incomplete", name)
		}
	}
}

func TestProviderUsageAndCost(t *testing.T) {
	for name, tc := range map[string]struct {
		body, raw, amount, unit string
		openRouter              bool
	}{
		"kiro": {"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"kiro:credits\":{\"value\":0.0283}}}}",
			`{"kiro:credits":{"value":0.0283}}`, "0.0283", "credits", false},
		"openrouter":         {`{"usage":{"prompt_tokens":3,"cost":0.0012}}`, `{"prompt_tokens":3,"cost":0.0012}`, "0.0012", "credits", true},
		"other gateway cost": {`{"usage":{"prompt_tokens":3,"cost":0.0012}}`, `{"prompt_tokens":3,"cost":0.0012}`, "", "", false},
		"command code": {"data: {\"type\":\"finish\",\"totalUsage\":{\"inputTokens\":5}}\ndata: {\"type\":\"provider-metadata\",\"providerMetadata\":{\"gateway\":{\"cost\":\"0.000296728\"}}}",
			`{"inputTokens":5}`, "0.000296728", "usd", false},
		"claude stream": {"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":1}}}\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}",
			`{"input_tokens":9,"output_tokens":7}`, "", "", false},
		"no cost": {`{"usage":{"input_tokens":4}}`, `{"input_tokens":4}`, "", "", false},
	} {
		raw, amount, unit := providerUsage(documents([]byte(tc.body), false), tc.openRouter)
		if string(raw) != tc.raw || amount != tc.amount || unit != tc.unit {
			t.Errorf("%s: %s %q %q, want %s %q %q", name, raw, amount, unit, tc.raw, tc.amount, tc.unit)
		}
	}
}

func TestTruncationLeavesNoSecretPrefix(t *testing.T) {
	const secret = "sk-synthetic-0123456789"
	for _, tc := range []struct{ data, want string }{
		{`{"key":"sk-synthetic-01`, `{"key":"[credential]`},
		{`{"key":"sk-synth`, `{"key":"[credential]`},
		{`{"key":"sk-synt`, `{"key":"sk-synt`},
		{`{"text":"ends with s`, `{"text":"ends with s`},
	} {
		if got := string(trimSecretPrefix([]byte(tc.data), secret)); got != tc.want {
			t.Errorf("trimSecretPrefix(%q) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestBodyKeepsItsLimitAndDescribesTheWhole(t *testing.T) {
	b := newBody("application/json", 4)
	b.write([]byte("abcdef"))
	p := b.payload(KindClientInput)
	if string(p.Body) != "abcd" || !p.Truncated || p.OriginalBytes != 6 || len(p.SHA256) != 32 {
		t.Fatalf("payload = %q truncated %v bytes %d", p.Body, p.Truncated, p.OriginalBytes)
	}
	whole := newBody("application/json", 16)
	whole.write([]byte("abcdef"))
	if string(whole.payload(KindClientInput).SHA256) != string(p.SHA256) {
		t.Fatal("a truncated payload's digest is not the full body's")
	}
}
