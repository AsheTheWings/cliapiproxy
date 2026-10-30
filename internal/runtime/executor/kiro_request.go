package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/tidwall/gjson"
)

// kiroInstructionsAck is the assistant turn that follows the system text, as
// the Kiro agent sends it.
const kiroInstructionsAck = "I will follow these instructions."

// kiroIDNamespace derives conversation identifiers from session identities.
var kiroIDNamespace = uuid.MustParse("5b0c6f3e-7a52-4e0b-9c53-2f6f1f0d8a41")

// kiroRequestInput is what the Kiro request body is built from.
type kiroRequestInput struct {
	// Body is the request in Responses format, after thinking was applied.
	Body []byte
	// Model is the upstream model id.
	Model string
	// SessionID is the client session the request belongs to; empty gives each
	// request its own conversation.
	SessionID string
	// CatalogModel is the model's catalog entry, when the credential has one.
	CatalogModel *kiroauth.CatalogModel
}

type kiroPart struct {
	text     string
	imageURL string
}

type kiroItem struct {
	kind      string // "message", "call", "output", "reasoning"
	role      string
	parts     []kiroPart
	callID    string
	name      string
	arguments string
	output    string
	encrypted string
}

// buildKiroRequest turns a Responses-format request into the request the Kiro
// inference route accepts. The route takes no system text, no tool choice, and
// no sampling or output limits, so the body carries only the fields below.
func buildKiroRequest(in kiroRequestInput) ([]byte, error) {
	root := gjson.ParseBytes(in.Body)
	var reasoning kiroauth.Reasoning
	if in.CatalogModel != nil {
		reasoning = in.CatalogModel.Reasoning()
	}

	var system []string
	if instructions := strings.TrimSpace(root.Get("instructions").String()); instructions != "" {
		system = append(system, instructions)
	}
	items, prompts, err := kiroInputItems(root.Get("input"), &system, reasoning.Style != kiroauth.ReasoningNone)
	if err != nil {
		return nil, err
	}
	items = kiroMergeMessages(kiroPairToolResults(items))
	if len(items) == 0 {
		return nil, statusErr{code: http.StatusBadRequest, msg: "kiro: the request has no input"}
	}
	if len(system) > 0 {
		lead := []kiroItem{
			{kind: "message", role: "user", parts: []kiroPart{{text: strings.Join(system, "\n\n")}}},
			{kind: "message", role: "assistant", parts: []kiroPart{{text: kiroInstructionsAck}}},
		}
		items = append(lead, items...)
	}

	inputJSON := kiroRenderItems(items)
	toolsJSON := kiroRenderTools(root.Get("tools"))

	var out bytes.Buffer
	out.WriteString(`{"model":`)
	out.Write(kiroMarshal(in.Model))
	out.WriteString(`,"input":`)
	out.Write(inputJSON)
	if toolsJSON != nil {
		out.WriteString(`,"tools":`)
		out.Write(toolsJSON)
	}
	out.WriteString(`,"stream":true,"origin":`)
	out.Write(kiroMarshal(kiroauth.Origin))
	out.WriteString(`,"metadata":`)
	out.Write(kiroMetadata(in.SessionID, prompts))
	if raw := kiroReasoningJSON(root, reasoning, in.CatalogModel != nil); raw != nil {
		out.WriteString(`,"reasoning":`)
		out.Write(raw)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func kiroMarshal(value any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return []byte("null")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// kiroMetadata names the conversation the way the agent does: one id for the
// session and one per user prompt, which a tool round trip shares.
func kiroMetadata(sessionID string, prompts int) []byte {
	conversation := "sess_" + uuid.NewString()
	continuation := uuid.NewString()
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		conversation = "sess_" + uuid.NewSHA1(kiroIDNamespace, []byte("conversation|"+sessionID)).String()
		continuation = uuid.NewSHA1(kiroIDNamespace, []byte("prompt|"+sessionID+"|"+strconv.Itoa(prompts))).String()
	}
	var out bytes.Buffer
	out.WriteString(`{"kiro:agent_mode":"vibe","kiro:conversation_id":`)
	out.Write(kiroMarshal(conversation))
	out.WriteString(`,"kiro:root_conversation_id":`)
	out.Write(kiroMarshal(conversation))
	out.WriteString(`,"kiro:agent_continuation_id":`)
	out.Write(kiroMarshal(continuation))
	out.WriteByte('}')
	return out.Bytes()
}

// kiroInputItems reads the Responses input list. System and developer text
// before the first other item joins system; later system text becomes a user
// message. prompts counts the user messages.
func kiroInputItems(input gjson.Result, system *[]string, reasoningAllowed bool) ([]kiroItem, int, error) {
	if input.Type == gjson.String {
		return []kiroItem{{kind: "message", role: "user", parts: []kiroPart{{text: input.String()}}}}, 1, nil
	}
	var items []kiroItem
	prompts := 0
	seenReasoning := make(map[string]struct{})
	for _, node := range input.Array() {
		itemType := node.Get("type").String()
		switch {
		case itemType == "function_call":
			callID := strings.TrimSpace(node.Get("call_id").String())
			name := strings.TrimSpace(node.Get("name").String())
			if callID == "" || name == "" {
				continue
			}
			items = append(items, kiroItem{kind: "call", callID: callID, name: name, arguments: kiroJSONText(node.Get("arguments"), "{}")})
		case itemType == "function_call_output":
			callID := strings.TrimSpace(node.Get("call_id").String())
			if callID == "" {
				continue
			}
			items = append(items, kiroItem{kind: "output", callID: callID, output: kiroJSONText(node.Get("output"), "")})
		case itemType == "reasoning":
			encrypted := node.Get("encrypted_content").String()
			if !reasoningAllowed || !kiroReplayableReasoning(encrypted) {
				continue
			}
			if _, dup := seenReasoning[encrypted]; dup {
				continue
			}
			seenReasoning[encrypted] = struct{}{}
			items = append(items, kiroItem{kind: "reasoning", encrypted: encrypted})
		case itemType == "message" || (itemType == "" && node.Get("role").Exists()):
			role := node.Get("role").String()
			parts, err := kiroMessageParts(node.Get("content"), role)
			if err != nil {
				return nil, 0, err
			}
			switch role {
			case "system", "developer":
				if len(items) == 0 {
					if text := kiroPartsText(parts); text != "" {
						*system = append(*system, text)
					}
					continue
				}
				role = "user"
			case "user":
				prompts++
			case "assistant":
			default:
				continue
			}
			if len(parts) == 0 {
				continue
			}
			items = append(items, kiroItem{kind: "message", role: role, parts: parts})
		}
	}
	return items, prompts, nil
}

// kiroJSONText returns a field that the route takes as a string. A value that
// is not a string travels as its JSON text.
func kiroJSONText(value gjson.Result, fallback string) string {
	switch {
	case !value.Exists() || value.Type == gjson.Null:
		return fallback
	case value.Type == gjson.String:
		return value.String()
	default:
		return value.Raw
	}
}

// kiroReplayableReasoning reports whether encrypted is a reasoning item the
// Kiro service produced: a JSON object carrying a signature. The service
// rejects any other item, so the request omits it.
func kiroReplayableReasoning(encrypted string) bool {
	if encrypted == "" || !gjson.Valid(encrypted) {
		return false
	}
	return strings.TrimSpace(gjson.Get(encrypted, "reasoningText.signature").String()) != ""
}

func kiroMessageParts(content gjson.Result, role string) ([]kiroPart, error) {
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil, nil
		}
		return []kiroPart{{text: content.String()}}, nil
	}
	var parts []kiroPart
	for _, part := range content.Array() {
		switch kind := part.Get("type").String(); kind {
		case "input_text", "output_text", "text":
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, kiroPart{text: text})
			}
		case "refusal":
			if text := part.Get("refusal").String(); text != "" {
				parts = append(parts, kiroPart{text: text})
			}
		case "input_image":
			if role == "assistant" {
				continue
			}
			imageURL := part.Get("image_url")
			if imageURL.IsObject() {
				imageURL = imageURL.Get("url")
			}
			value := strings.TrimSpace(imageURL.String())
			if !strings.HasPrefix(value, "data:") {
				return nil, statusErr{code: http.StatusBadRequest, msg: "kiro: images must be sent as data URIs"}
			}
			parts = append(parts, kiroPart{imageURL: value})
		default:
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("kiro: content part type %q is not supported", kind)}
		}
	}
	return parts, nil
}

func kiroPartsText(parts []kiroPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.imageURL == "" && part.text != "" {
			texts = append(texts, part.text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// kiroMergeMessages joins neighbouring messages of one role, so the history
// alternates the way the service expects.
func kiroMergeMessages(items []kiroItem) []kiroItem {
	merged := make([]kiroItem, 0, len(items))
	for _, item := range items {
		if last := len(merged) - 1; last >= 0 && item.kind == "message" &&
			merged[last].kind == "message" && merged[last].role == item.role {
			merged[last].parts = append(append([]kiroPart(nil), merged[last].parts...), item.parts...)
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

// kiroPairToolResults orders each run of tool calls and results as the service
// requires on every model family: the calls, then one result per call in call
// order. A result without a call, a repeated result, and a call without a
// result are dropped.
func kiroPairToolResults(items []kiroItem) []kiroItem {
	out := make([]kiroItem, 0, len(items))
	for start := 0; start < len(items); {
		if items[start].kind != "call" && items[start].kind != "output" {
			out = append(out, items[start])
			start++
			continue
		}
		end := start
		for end < len(items) && (items[end].kind == "call" || items[end].kind == "output") {
			end++
		}
		var calls []kiroItem
		outputs := make(map[string]kiroItem)
		for _, item := range items[start:end] {
			if item.kind == "call" {
				calls = append(calls, item)
			} else if _, dup := outputs[item.callID]; !dup {
				outputs[item.callID] = item
			}
		}
		var answered []kiroItem
		for _, call := range calls {
			if _, ok := outputs[call.callID]; ok {
				answered = append(answered, call)
			}
		}
		out = append(out, answered...)
		for _, call := range answered {
			if result, ok := outputs[call.callID]; ok {
				out = append(out, result)
				delete(outputs, call.callID)
			}
		}
		start = end
	}
	return out
}

func kiroRenderItems(items []kiroItem) []byte {
	var out bytes.Buffer
	out.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			out.WriteByte(',')
		}
		switch item.kind {
		case "message":
			out.WriteString(`{"role":`)
			out.Write(kiroMarshal(item.role))
			out.WriteString(`,"content":`)
			out.Write(kiroRenderContent(item))
			out.WriteByte('}')
		case "call":
			out.WriteString(`{"type":"function_call","call_id":`)
			out.Write(kiroMarshal(item.callID))
			out.WriteString(`,"name":`)
			out.Write(kiroMarshal(item.name))
			out.WriteString(`,"arguments":`)
			out.Write(kiroMarshal(item.arguments))
			out.WriteByte('}')
		case "output":
			out.WriteString(`{"type":"function_call_output","call_id":`)
			out.Write(kiroMarshal(item.callID))
			out.WriteString(`,"output":`)
			out.Write(kiroMarshal(item.output))
			out.WriteByte('}')
		case "reasoning":
			out.WriteString(`{"type":"reasoning","summary":[],"encrypted_content":`)
			out.Write(kiroMarshal(item.encrypted))
			out.WriteByte('}')
		}
	}
	out.WriteByte(']')
	return out.Bytes()
}

// kiroRenderContent writes a message's content: a string when it is all text,
// else the list of input parts.
func kiroRenderContent(item kiroItem) []byte {
	hasImage := false
	for _, part := range item.parts {
		if part.imageURL != "" {
			hasImage = true
		}
	}
	if !hasImage || item.role == "assistant" {
		return kiroMarshal(kiroPartsText(item.parts))
	}
	var out bytes.Buffer
	out.WriteByte('[')
	for i, part := range item.parts {
		if i > 0 {
			out.WriteByte(',')
		}
		if part.imageURL != "" {
			out.WriteString(`{"type":"input_image","image_url":`)
			out.Write(kiroMarshal(part.imageURL))
		} else {
			out.WriteString(`{"type":"input_text","text":`)
			out.Write(kiroMarshal(part.text))
		}
		out.WriteByte('}')
	}
	out.WriteByte(']')
	return out.Bytes()
}

// kiroRenderTools writes the function tools. The route takes only function
// tools, so hosted and freeform tools are left out.
func kiroRenderTools(tools gjson.Result) []byte {
	var out bytes.Buffer
	count := 0
	out.WriteByte('[')
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			continue
		}
		parameters := tool.Get("parameters")
		parametersRaw := `{"type":"object","properties":{}}`
		if parameters.IsObject() {
			parametersRaw = parameters.Raw
		}
		if count > 0 {
			out.WriteByte(',')
		}
		count++
		out.WriteString(`{"type":"function","name":`)
		out.Write(kiroMarshal(name))
		out.WriteString(`,"description":`)
		out.Write(kiroMarshal(tool.Get("description").String()))
		out.WriteString(`,"parameters":`)
		out.WriteString(parametersRaw)
		out.WriteByte('}')
	}
	out.WriteByte(']')
	if count == 0 {
		return nil
	}
	return out.Bytes()
}

// kiroReasoningJSON writes the reasoning field. The agent sends it for every
// model whose catalog entry has an effort schema, with the schema's default when
// the client chose none.
func kiroReasoningJSON(root gjson.Result, reasoning kiroauth.Reasoning, known bool) []byte {
	if !known || reasoning.Style == kiroauth.ReasoningNone {
		return nil
	}
	effort := strings.ToLower(strings.TrimSpace(root.Get("reasoning.effort").String()))
	if effort == "none" || effort == "disabled" {
		if !reasoning.CanDisable {
			effort = ""
		} else if reasoning.Style == kiroauth.ReasoningAdaptive {
			return []byte(`{"type":"disabled"}`)
		} else {
			return []byte(`{"effort":"none"}`)
		}
	}
	if !kiroContains(reasoning.Levels, effort) {
		effort = reasoning.Default
		if !kiroContains(reasoning.Levels, effort) {
			switch {
			case kiroContains(reasoning.Levels, "medium"):
				effort = "medium"
			case len(reasoning.Levels) > 0:
				effort = reasoning.Levels[0]
			default:
				return nil
			}
		}
	}
	if reasoning.Style == kiroauth.ReasoningAdaptive {
		return []byte(`{"type":"adaptive","effort":` + string(kiroMarshal(effort)) + `}`)
	}
	return []byte(`{"effort":` + string(kiroMarshal(effort)) + `}`)
}

func kiroContains(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}
