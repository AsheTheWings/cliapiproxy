package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	commandcodeauth "github.com/AsheTheWings/cliapiproxy/internal/auth/commandcode"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	// commandCodeDefaultMaxTokens is the output limit the CLI sends.
	commandCodeDefaultMaxTokens = 64000
	// commandCodeMaxTokensCeiling is the largest limit the gateway accepts.
	commandCodeMaxTokensCeiling = 200000
	// commandCodeMaxCallIDLength is the longest tool call id the gateway accepts.
	commandCodeMaxCallIDLength = 64
	// commandCodeDefaultSystem is the system text of a request whose client sent
	// none; the gateway would otherwise apply the prompt of its own coding agent.
	commandCodeDefaultSystem = "You are a helpful assistant."
	// commandCodeImageOmitted replaces an image the model cannot read.
	commandCodeImageOmitted = "[image omitted: the model does not accept images]"
	// commandCodeReasoningKey names the reasoning text in the encrypted
	// content of a reasoning item the proxy returned.
	commandCodeReasoningKey = "commandcode_reasoning"
)

// commandCodeIDNamespace derives thread identifiers from session identities.
var commandCodeIDNamespace = uuid.MustParse("c3a6e5d2-0a43-4f0e-8d7b-9e1f6a2b4c58")

// commandCodeRequestInput is what the gateway request body is built from.
type commandCodeRequestInput struct {
	// Body is the request in Responses format, after thinking was applied.
	Body []byte
	// Model is the gateway model id.
	Model string
	// SessionID is the client session the request belongs to; empty gives each
	// request its own thread.
	SessionID string
	// CatalogModel is the model's catalog entry, when the credential has one.
	CatalogModel *commandcodeauth.CatalogModel
	// MaxTokens is the output limit the client asked for; zero means none.
	MaxTokens int64
	// Now dates the request.
	Now time.Time
}

// commandCodeRequest is the built request: the body and the thread it names.
type commandCodeRequest struct {
	Body     []byte
	ThreadID string
}

type commandCodePart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Image      string          `json:"image,omitempty"`
	MimeType   string          `json:"mimeType,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     *commandCodeOut `json:"output,omitempty"`
}

type commandCodeOut struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type commandCodeMessage struct {
	Role    string            `json:"role"`
	Content []commandCodePart `json:"content"`
}

type commandCodeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type commandCodeSystem struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl map[string]string `json:"cache_control"`
}

// commandCodeItem is one element of the conversation before it is grouped
// into wire messages.
type commandCodeItem struct {
	kind   string // "user", "assistant", "reasoning", "call", "output"
	parts  []commandCodePart
	callID string
	name   string
	args   string
	output string
}

// buildCommandCodeRequest turns a Responses-format request into the request
// the gateway's agent route accepts. The route takes no tool choice and no
// sampling parameters of the CLI's own, so the body carries only the fields of
// the CLI's request.
func buildCommandCodeRequest(in commandCodeRequestInput) (*commandCodeRequest, error) {
	root := gjson.ParseBytes(in.Body)
	supportsImages := in.CatalogModel == nil || in.CatalogModel.SupportsImages()

	var system []string
	if instructions := strings.TrimSpace(root.Get("instructions").String()); instructions != "" {
		system = append(system, instructions)
	}
	items, err := commandCodeInputItems(root.Get("input"), &system, supportsImages)
	if err != nil {
		return nil, err
	}
	items = commandCodePairToolResults(items)
	messages := commandCodeMessages(items)
	if len(messages) == 0 {
		return nil, statusErr{code: http.StatusBadRequest, msg: "commandcode: the request has no input"}
	}

	systemText := strings.Join(system, "\n\n")
	if strings.TrimSpace(systemText) == "" {
		systemText = commandCodeDefaultSystem
	}
	params := map[string]any{
		"model":      in.Model,
		"messages":   messages,
		"tools":      commandCodeTools(root.Get("tools")),
		"system":     []commandCodeSystem{{Type: "text", Text: systemText, CacheControl: map[string]string{"type": "ephemeral"}}},
		"max_tokens": commandCodeMaxTokens(in.MaxTokens, in.CatalogModel),
		"stream":     true,
	}
	if effort := commandCodeEffort(root, in.CatalogModel); effort != "" {
		params["reasoning_effort"] = effort
	}

	threadID := uuid.NewString()
	if session := strings.TrimSpace(in.SessionID); session != "" {
		threadID = uuid.NewSHA1(commandCodeIDNamespace, []byte("thread|"+session)).String()
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	body := map[string]any{
		"config": map[string]any{
			"workingDir":    "/work",
			"date":          now.UTC().Format("2006-01-02"),
			"environment":   "linux",
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory":         nil,
		"taste":          nil,
		"skills":         nil,
		"permissionMode": "standard",
		"threadId":       threadID,
		"params":         params,
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(body); err != nil {
		return nil, err
	}
	return &commandCodeRequest{Body: bytes.TrimRight(out.Bytes(), "\n"), ThreadID: threadID}, nil
}

// commandCodeMaxTokens is the output limit: the client's, else the CLI's, kept
// within the model's and the gateway's.
func commandCodeMaxTokens(requested int64, model *commandcodeauth.CatalogModel) int64 {
	limit := requested
	if limit <= 0 {
		limit = commandCodeDefaultMaxTokens
	}
	if model != nil && model.MaxOutputTokens > 0 && limit > int64(model.MaxOutputTokens) {
		limit = int64(model.MaxOutputTokens)
	}
	if limit > commandCodeMaxTokensCeiling {
		limit = commandCodeMaxTokensCeiling
	}
	return limit
}

// commandCodeEffort is the reasoning effort the request names, when the model
// takes that effort.
func commandCodeEffort(root gjson.Result, model *commandcodeauth.CatalogModel) string {
	if model == nil {
		return ""
	}
	levels, canDisable := model.Efforts()
	effort := strings.TrimSpace(root.Get("reasoning.effort").String())
	switch {
	case effort == "":
		return ""
	case effort == "none":
		if canDisable {
			return "off"
		}
		return ""
	}
	for _, level := range levels {
		if level == effort {
			return effort
		}
	}
	return ""
}

func commandCodeTools(tools gjson.Result) []commandCodeTool {
	out := []commandCodeTool{}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			continue
		}
		schema := json.RawMessage(`{"type":"object","properties":{}}`)
		if parameters := tool.Get("parameters"); parameters.IsObject() {
			schema = json.RawMessage(parameters.Raw)
		}
		out = append(out, commandCodeTool{Name: name, Description: tool.Get("description").String(), InputSchema: schema})
	}
	return out
}

// commandCodeInputItems reads the Responses input list. System and developer
// text before the first other item joins system; later system text becomes a
// user message.
func commandCodeInputItems(input gjson.Result, system *[]string, supportsImages bool) ([]commandCodeItem, error) {
	if input.Type == gjson.String {
		return []commandCodeItem{{kind: "user", parts: []commandCodePart{{Type: "text", Text: input.String()}}}}, nil
	}
	var items []commandCodeItem
	for _, node := range input.Array() {
		itemType := node.Get("type").String()
		switch {
		case itemType == "function_call":
			callID := strings.TrimSpace(node.Get("call_id").String())
			name := strings.TrimSpace(node.Get("name").String())
			if callID == "" || name == "" {
				continue
			}
			items = append(items, commandCodeItem{kind: "call", callID: callID, name: name, args: commandCodeText(node.Get("arguments"), "{}")})
		case itemType == "function_call_output":
			callID := strings.TrimSpace(node.Get("call_id").String())
			if callID == "" {
				continue
			}
			items = append(items, commandCodeItem{kind: "output", callID: callID, output: commandCodeOutputText(node.Get("output"))})
		case itemType == "reasoning":
			if text := commandCodeReasoningText(node); text != "" {
				items = append(items, commandCodeItem{kind: "reasoning", parts: []commandCodePart{{Type: "reasoning", Text: text}}})
			}
		case itemType == "message" || (itemType == "" && node.Get("role").Exists()):
			role := node.Get("role").String()
			parts, err := commandCodeMessageParts(node.Get("content"), role, supportsImages)
			if err != nil {
				return nil, err
			}
			kind := role
			switch role {
			case "system", "developer":
				if len(items) == 0 {
					if text := commandCodePartsText(parts); text != "" {
						*system = append(*system, text)
					}
					continue
				}
				kind = "user"
			case "user", "assistant":
			default:
				continue
			}
			if len(parts) == 0 {
				continue
			}
			items = append(items, commandCodeItem{kind: kind, parts: parts})
		}
	}
	return items, nil
}

// commandCodeText returns a value that the wire takes as text. A value that is
// not a string travels as its JSON text.
func commandCodeText(value gjson.Result, fallback string) string {
	switch {
	case !value.Exists() || value.Type == gjson.Null:
		return fallback
	case value.Type == gjson.String:
		return value.String()
	default:
		return value.Raw
	}
}

// commandCodeOutputText is the text of a tool result. A list of content parts
// contributes its text parts.
func commandCodeOutputText(output gjson.Result) string {
	if output.IsArray() {
		var texts []string
		for _, part := range output.Array() {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return commandCodeText(output, "")
}

// commandCodeReasoningText is the reasoning text of a Responses reasoning item:
// the text the proxy stored in the item's encrypted content, else the item's
// summary text.
func commandCodeReasoningText(node gjson.Result) string {
	if encrypted := node.Get("encrypted_content").String(); encrypted != "" && gjson.Valid(encrypted) {
		if text := gjson.Get(encrypted, commandCodeReasoningKey).String(); text != "" {
			return text
		}
	}
	var texts []string
	for _, part := range node.Get("summary").Array() {
		if text := part.Get("text").String(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n\n")
}

func commandCodeMessageParts(content gjson.Result, role string, supportsImages bool) ([]commandCodePart, error) {
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil, nil
		}
		return []commandCodePart{{Type: "text", Text: content.String()}}, nil
	}
	var parts []commandCodePart
	for _, part := range content.Array() {
		switch kind := part.Get("type").String(); kind {
		case "input_text", "output_text", "text":
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, commandCodePart{Type: "text", Text: text})
			}
		case "refusal":
			if text := part.Get("refusal").String(); text != "" {
				parts = append(parts, commandCodePart{Type: "text", Text: text})
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
			mime, ok := commandCodeDataURIMime(value)
			if !ok {
				return nil, statusErr{code: http.StatusBadRequest, msg: "commandcode: images must be sent as data URIs"}
			}
			if !supportsImages {
				parts = append(parts, commandCodePart{Type: "text", Text: commandCodeImageOmitted})
				continue
			}
			parts = append(parts, commandCodePart{Type: "image", Image: value, MimeType: mime})
		default:
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("commandcode: content part type %q is not supported", kind)}
		}
	}
	return parts, nil
}

// commandCodeDataURIMime returns the media type of a base64 data URI.
func commandCodeDataURIMime(value string) (string, bool) {
	rest, ok := strings.CutPrefix(value, "data:")
	if !ok {
		return "", false
	}
	header, _, found := strings.Cut(rest, ",")
	if !found {
		return "", false
	}
	mime, _, _ := strings.Cut(header, ";")
	if strings.TrimSpace(mime) == "" {
		return "", false
	}
	return strings.TrimSpace(mime), true
}

func commandCodePartsText(parts []commandCodePart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// commandCodePairToolResults orders each run of tool calls and results as the
// providers behind the gateway require: the calls, then one result per call in
// call order. A call without a result, a result without a call, and a repeated
// result are dropped.
func commandCodePairToolResults(items []commandCodeItem) []commandCodeItem {
	out := make([]commandCodeItem, 0, len(items))
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
		outputs := make(map[string]commandCodeItem)
		for _, item := range items[start:end] {
			if _, dup := outputs[item.callID]; item.kind == "output" && !dup {
				outputs[item.callID] = item
			}
		}
		var answered []commandCodeItem
		seen := make(map[string]bool)
		for _, item := range items[start:end] {
			if _, ok := outputs[item.callID]; item.kind == "call" && ok && !seen[item.callID] {
				seen[item.callID] = true
				answered = append(answered, item)
			}
		}
		out = append(out, answered...)
		for _, call := range answered {
			out = append(out, outputs[call.callID])
		}
		start = end
	}
	return out
}

// commandCodeMessages groups the conversation into wire messages: the reasoning,
// text, and calls of one assistant turn form one assistant message, and the
// results that follow form one tool message. Neighbouring user messages join.
func commandCodeMessages(items []commandCodeItem) []commandCodeMessage {
	// An alias skips every id the conversation already uses, so it never
	// joins a call to another call's result.
	used := make(map[string]bool)
	for _, item := range items {
		if item.callID != "" {
			used[item.callID] = true
		}
	}
	aliases := make(map[string]string)
	next := 0
	wireID := func(id string) string {
		if len(id) <= commandCodeMaxCallIDLength {
			return id
		}
		if alias, ok := aliases[id]; ok {
			return alias
		}
		var alias string
		for {
			next++
			if alias = "cc-" + strconv.Itoa(next); !used[alias] {
				break
			}
		}
		used[alias] = true
		aliases[id] = alias
		return alias
	}
	toolNames := make(map[string]string)
	for _, item := range items {
		if item.kind == "call" {
			toolNames[item.callID] = item.name
		}
	}

	var messages []commandCodeMessage
	appendPart := func(role string, part commandCodePart) {
		if last := len(messages) - 1; last >= 0 && messages[last].Role == role {
			messages[last].Content = append(messages[last].Content, part)
			return
		}
		messages = append(messages, commandCodeMessage{Role: role, Content: []commandCodePart{part}})
	}
	for _, item := range items {
		switch item.kind {
		case "user":
			for _, part := range item.parts {
				appendPart("user", part)
			}
		case "assistant", "reasoning":
			for _, part := range item.parts {
				appendPart("assistant", part)
			}
		case "call":
			input := json.RawMessage(`{}`)
			if parsed := gjson.Parse(item.args); parsed.IsObject() {
				input = json.RawMessage(parsed.Raw)
			}
			appendPart("assistant", commandCodePart{Type: "tool-call", ToolCallID: wireID(item.callID), ToolName: item.name, Input: input})
		case "output":
			value := item.output
			if value == "" {
				value = "(empty tool result)"
			}
			appendPart("tool", commandCodePart{
				Type: "tool-result", ToolCallID: wireID(item.callID), ToolName: toolNames[item.callID],
				Output: &commandCodeOut{Type: "text", Value: value},
			})
		}
	}
	return messages
}
