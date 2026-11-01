package capture

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// redactedCredential replaces a credential found in an upstream body.
const redactedCredential = "[credential]"

// minSecretLength keeps short values, which could match ordinary text, from
// being treated as secrets.
const minSecretLength = 8

func replaceSecret(data []byte, secret string) []byte {
	if len(secret) < minSecretLength {
		return data
	}
	return bytes.ReplaceAll(data, []byte(secret), []byte(redactedCredential))
}

// documents splits a response into its JSON documents: the whole body, or
// for a stream or frame log each line, without an SSE "data:" prefix.
func documents(data []byte, framed bool) []gjson.Result {
	if !framed {
		if doc := gjson.ParseBytes(data); doc.IsObject() {
			return []gjson.Result{doc}
		}
		framed = true
	}
	var out []gjson.Result
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if doc := gjson.ParseBytes(line); doc.IsObject() {
			out = append(out, doc)
		}
	}
	return out
}

// incomplete reports an upstream stop at the output limit: a Responses
// response.incomplete event or incomplete status, a Claude max_tokens stop,
// a Gemini MAX_TOKENS finish, or a length finish of OpenAI Chat or
// Command Code.
func incomplete(docs []gjson.Result) bool {
	for _, doc := range docs {
		switch {
		case doc.Get("type").String() == "response.incomplete",
			doc.Get("response.status").String() == "incomplete",
			doc.Get("object").String() == "response" && doc.Get("status").String() == "incomplete",
			doc.Get("stop_reason").String() == "max_tokens",
			doc.Get("delta.stop_reason").String() == "max_tokens",
			doc.Get("finishReason").String() == "length",
			anyEquals(doc.Get("choices.#.finish_reason"), "length"),
			anyEquals(doc.Get("candidates.#.finishReason"), "MAX_TOKENS"),
			anyEquals(doc.Get("response.candidates.#.finishReason"), "MAX_TOKENS"):
			return true
		}
	}
	return false
}

func anyEquals(list gjson.Result, want string) bool {
	for _, v := range list.Array() {
		if v.String() == want {
			return true
		}
	}
	return false
}

// usagePaths locate a provider's usage object in a response document.
var usagePaths = []string{"usage", "response.usage", "usageMetadata", "response.usageMetadata", "message.usage", "totalUsage"}

// providerUsage returns the response's usage, its usage objects merged in
// order so a later field replaces an earlier one, as a Claude stream's
// message_delta completes its message_start, and the cost it carries:
// Kiro's kiro:credits value and, from OpenRouter, usage.cost in credits,
// and Command Code's gateway cost in dollars.
func providerUsage(docs []gjson.Result, openRouter bool) (raw []byte, amount, unit string) {
	merged := map[string]json.RawMessage{}
	var order []string
	for _, doc := range docs {
		for _, path := range usagePaths {
			if u := doc.Get(path); u.IsObject() {
				u.ForEach(func(key, value gjson.Result) bool {
					if _, seen := merged[key.String()]; !seen {
						order = append(order, key.String())
					}
					merged[key.String()] = json.RawMessage(value.Raw)
					return true
				})
				if v := u.Get(`kiro:credits.value`); v.Type == gjson.Number {
					amount, unit = v.Raw, "credits"
				} else if v = u.Get("cost"); openRouter && v.Type == gjson.Number {
					amount, unit = v.Raw, "credits"
				}
				break
			}
		}
		if doc.Get("type").String() == "provider-metadata" {
			if v := doc.Get("providerMetadata.gateway.cost"); v.Exists() && numeric(v.String()) {
				amount, unit = v.String(), "usd"
			}
		}
	}
	if len(order) > 0 {
		var b bytes.Buffer
		b.WriteByte('{')
		for i, key := range order {
			if i > 0 {
				b.WriteByte(',')
			}
			name, _ := json.Marshal(key)
			b.Write(name)
			b.WriteByte(':')
			b.Write(merged[key])
		}
		b.WriteByte('}')
		raw = b.Bytes()
	}
	return raw, amount, unit
}

// isOpenRouter reports an upstream base URL on OpenRouter.
func isOpenRouter(baseURL string) bool {
	return strings.Contains(strings.ToLower(baseURL), "openrouter.ai")
}

func numeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return gjson.Parse(s).Type == gjson.Number
}

// systemInstruction returns the system instruction of a client input in the
// OpenAI Chat, Responses, Claude, or Gemini format, its parts joined by
// blank lines, or "".
func systemInstruction(input []byte) string {
	doc := gjson.ParseBytes(input)
	if !doc.IsObject() {
		return ""
	}
	var parts []string
	var add func(v gjson.Result)
	add = func(v gjson.Result) {
		switch {
		case v.Type == gjson.String:
			if s := strings.TrimSpace(v.String()); s != "" {
				parts = append(parts, v.String())
			}
		case v.IsArray():
			for _, item := range v.Array() {
				if item.Type == gjson.String {
					add(item)
					continue
				}
				add(item.Get("text"))
			}
		}
	}
	add(doc.Get("instructions"))
	add(doc.Get("system"))
	add(doc.Get("systemInstruction.parts"))
	add(doc.Get("system_instruction.parts"))
	for _, list := range []string{"messages", "input"} {
		for _, m := range doc.Get(list).Array() {
			if role := m.Get("role").String(); role == "system" || role == "developer" {
				add(m.Get("content"))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
