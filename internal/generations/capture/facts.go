package capture

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// responseFacts keeps structured facts independently of the stored body's
// byte limit. It retains usage fields, not response text or output items.
type responseFacts struct {
	terminal, incomplete bool
	usage                map[string]json.RawMessage
	amount, unit         string
	routerCost           bool
	data                 []byte
	fragmentLine         []byte
	event                string
}

// fragment consumes arbitrary transport bytes without inventing line breaks.
func (f *responseFacts) fragment(chunk []byte) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			f.fragmentLine = append(f.fragmentLine, chunk...)
			return
		}
		if len(f.fragmentLine) > 0 {
			f.fragmentLine = append(f.fragmentLine, chunk[:i]...)
			f.observe(f.fragmentLine)
			f.fragmentLine = nil
		} else {
			f.observe(chunk[:i])
		}
		chunk = chunk[i+1:]
	}
}

func (f *responseFacts) finish() {
	if len(f.fragmentLine) > 0 {
		f.observe(f.fragmentLine)
		f.fragmentLine = nil
	}
}

// observe consumes the complete body or scanner lines reported by a hook.
func (f *responseFacts) observe(chunk []byte) {
	if len(f.data) == 0 && gjson.ValidBytes(chunk) {
		f.data = nil
		f.document(gjson.ParseBytes(chunk))
		return
	}
	for _, line := range bytes.Split(chunk, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			f.data = nil
			f.event = ""
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			f.event = strings.TrimSpace(string(line[len("event:"):]))
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		} else if line[0] != '{' && len(f.data) == 0 {
			continue
		}
		if bytes.Equal(line, []byte("[DONE]")) {
			f.terminal = true
			f.data = nil
			continue
		}
		if len(f.data) > 0 {
			f.data = append(f.data, '\n')
		}
		f.data = append(f.data, line...)
		if gjson.ValidBytes(f.data) {
			f.document(gjson.ParseBytes(f.data))
			f.data = nil
		}
	}
}

func (f *responseFacts) document(doc gjson.Result) {
	if !doc.IsObject() {
		return
	}
	f.terminal = f.terminal || terminal(doc)
	f.incomplete = f.incomplete || incomplete([]gjson.Result{doc})
	if terminalType(f.event) {
		f.terminal = true
		f.incomplete = f.incomplete || f.event == "response.incomplete"
	}
	raw, amount, unit := providerUsage([]gjson.Result{doc}, true)
	if len(raw) > 0 {
		// Unmarshal merges keys into the existing map, completing readings
		// such as Claude's message_start followed by message_delta.
		_ = json.Unmarshal(raw, &f.usage)
	}
	if amount != "" {
		f.amount, f.unit = amount, unit
		f.routerCost = unit == "credits"
		for _, path := range usagePaths {
			if doc.Get(path + ".kiro:credits.value").Exists() {
				f.routerCost = false
				break
			}
		}
	}
}

func (f *responseFacts) values(openRouter bool) ([]byte, string, string) {
	var raw []byte
	if len(f.usage) > 0 {
		raw, _ = json.Marshal(f.usage)
	}
	if f.routerCost && !openRouter {
		return raw, "", ""
	}
	return raw, f.amount, f.unit
}

// terminal recognizes a provider's terminal signal, before any client-side
// compatibility completion can be synthesized.
func terminal(doc gjson.Result) bool {
	if terminalType(doc.Get("type").String()) {
		return true
	}
	if doc.Get("event_type").String() == "interaction.completed" {
		return true
	}
	if doc.Get("object").String() == "response" {
		if status := doc.Get("status").String(); status == "completed" || status == "incomplete" {
			return true
		}
	}
	for _, path := range []string{"choices.#.finish_reason", "candidates.#.finishReason", "response.candidates.#.finishReason"} {
		for _, reason := range doc.Get(path).Array() {
			if s := strings.TrimSpace(reason.String()); reason.Type == gjson.String && s != "" && s != "FINISH_REASON_UNSPECIFIED" {
				return true
			}
		}
	}
	return false
}

func terminalType(kind string) bool {
	switch kind {
	case "response.completed", "response.incomplete", "message_stop", "finish", "image_generation.completed", "image_edit.completed":
		return true
	}
	return false
}
