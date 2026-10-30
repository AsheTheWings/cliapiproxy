package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// maxCommandCodeLine bounds one line of the gateway's stream.
const maxCommandCodeLine = 52_428_800

// commandCodeStream converts the gateway's stream, one JSON object per line,
// into a Responses event stream, so the executor reads it like the stream of
// any other Responses upstream.
type commandCodeStream struct {
	src    *bufio.Reader
	closer io.Closer
	out    bytes.Buffer
	err    error

	model      string
	responseID string
	createdAt  int64
	sequence   int
	started    bool
	finished   bool

	outputs []map[string]any

	reasoningID   string
	reasoning     strings.Builder
	reasoningOpen bool
	messageID     string
	message       strings.Builder
	messageOpen   bool
}

// newCommandCodeStream wraps the body of a gateway response. model is the
// model id the response reports.
func newCommandCodeStream(body io.ReadCloser, model string) *commandCodeStream {
	return &commandCodeStream{
		src:        bufio.NewReaderSize(body, 64<<10),
		closer:     body,
		model:      model,
		responseID: "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		createdAt:  time.Now().Unix(),
	}
}

// Read returns the converted stream.
func (s *commandCodeStream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.src.ReadBytes('\n')
		if len(line) > maxCommandCodeLine {
			s.err = errors.New("commandcode: a line of the stream exceeds the size limit")
			continue
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && !s.finished {
			s.convert(trimmed)
		}
		if err != nil {
			s.err = err
		}
	}
	return s.out.Read(p)
}

// Close closes the response body.
func (s *commandCodeStream) Close() error { return s.closer.Close() }

func (s *commandCodeStream) emit(event map[string]any) {
	event["sequence_number"] = s.sequence
	s.sequence++
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(&s.out, "event: %s\ndata: %s\n\n", event["type"], data)
}

func (s *commandCodeStream) responseObject(status string) map[string]any {
	return map[string]any{
		"id": s.responseID, "object": "response", "created_at": s.createdAt,
		"status": status, "model": s.model, "output": []any{},
	}
}

// start emits the opening events of the response. It runs before the first
// output or outcome, not on the gateway's own start events, so a stream that
// fails before any output carries only the error and can run on another
// credential.
func (s *commandCodeStream) start() {
	if s.started {
		return
	}
	s.started = true
	s.emit(map[string]any{"type": "response.created", "response": s.responseObject("in_progress")})
	s.emit(map[string]any{"type": "response.in_progress", "response": s.responseObject("in_progress")})
}

func (s *commandCodeStream) newID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (s *commandCodeStream) openReasoning() {
	if s.reasoningOpen {
		return
	}
	s.start()
	s.closeMessage()
	s.reasoningOpen = true
	s.reasoningID = s.newID("rs")
	s.reasoning.Reset()
	s.emit(map[string]any{"type": "response.output_item.added", "output_index": len(s.outputs),
		"item": map[string]any{"id": s.reasoningID, "type": "reasoning", "summary": []any{}}})
	s.emit(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": s.reasoningID,
		"output_index": len(s.outputs), "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
}

func (s *commandCodeStream) closeReasoning() {
	if !s.reasoningOpen {
		return
	}
	s.reasoningOpen = false
	text := s.reasoning.String()
	index := len(s.outputs)
	part := map[string]any{"type": "summary_text", "text": text}
	envelope, _ := json.Marshal(map[string]string{commandCodeReasoningKey: text})
	item := map[string]any{"id": s.reasoningID, "type": "reasoning", "summary": []any{part}, "encrypted_content": string(envelope)}
	s.emit(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": s.reasoningID,
		"output_index": index, "summary_index": 0, "text": text})
	s.emit(map[string]any{"type": "response.reasoning_summary_part.done", "item_id": s.reasoningID,
		"output_index": index, "summary_index": 0, "part": part})
	s.emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	s.outputs = append(s.outputs, item)
}

func (s *commandCodeStream) openMessage() {
	if s.messageOpen {
		return
	}
	s.start()
	s.closeReasoning()
	s.messageOpen = true
	s.messageID = s.newID("msg")
	s.message.Reset()
	s.emit(map[string]any{"type": "response.output_item.added", "output_index": len(s.outputs),
		"item": map[string]any{"id": s.messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
	s.emit(map[string]any{"type": "response.content_part.added", "item_id": s.messageID, "output_index": len(s.outputs),
		"content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
}

func (s *commandCodeStream) closeMessage() {
	if !s.messageOpen {
		return
	}
	s.messageOpen = false
	text := s.message.String()
	index := len(s.outputs)
	part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
	item := map[string]any{"id": s.messageID, "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
	s.emit(map[string]any{"type": "response.output_text.done", "item_id": s.messageID, "output_index": index,
		"content_index": 0, "text": text})
	s.emit(map[string]any{"type": "response.content_part.done", "item_id": s.messageID, "output_index": index,
		"content_index": 0, "part": part})
	s.emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	s.outputs = append(s.outputs, item)
}

func (s *commandCodeStream) toolCall(event gjson.Result) {
	s.start()
	s.closeReasoning()
	s.closeMessage()
	callID := strings.TrimSpace(event.Get("toolCallId").String())
	if callID == "" {
		callID = s.newID("call")
	}
	name := event.Get("toolName").String()
	args := commandCodeToolArguments(event.Get("input"), event.Get("args"))
	itemID := s.newID("fc")
	index := len(s.outputs)
	s.emit(map[string]any{"type": "response.output_item.added", "output_index": index,
		"item": map[string]any{"id": itemID, "type": "function_call", "call_id": callID, "name": name, "arguments": "", "status": "in_progress"}})
	s.emit(map[string]any{"type": "response.function_call_arguments.delta", "item_id": itemID, "output_index": index, "delta": args})
	s.emit(map[string]any{"type": "response.function_call_arguments.done", "item_id": itemID, "output_index": index, "arguments": args})
	item := map[string]any{"id": itemID, "type": "function_call", "call_id": callID, "name": name, "arguments": args, "status": "completed"}
	s.emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	s.outputs = append(s.outputs, item)
}

// commandCodeToolArguments is the JSON object text of a tool call's input. The
// gateway sends an object; an array of one object, a JSON string, or nothing
// is read as the CLI reads it.
func commandCodeToolArguments(values ...gjson.Result) string {
	for _, value := range values {
		if !value.Exists() {
			continue
		}
		if value.IsArray() && len(value.Array()) == 1 {
			value = value.Array()[0]
		}
		if value.Type == gjson.String {
			if parsed := gjson.Parse(value.String()); parsed.IsObject() {
				return parsed.Raw
			}
			continue
		}
		if value.IsObject() {
			return value.Raw
		}
	}
	return "{}"
}

func (s *commandCodeStream) convert(line []byte) {
	if !gjson.ValidBytes(line) {
		return
	}
	event := gjson.ParseBytes(line)
	kind := event.Get("type").String()
	if kind == "error" {
		s.fail(event)
		return
	}
	switch kind {
	case "reasoning-start":
		s.openReasoning()
	case "reasoning-delta":
		if text := event.Get("text").String(); text != "" {
			s.openReasoning()
			s.reasoning.WriteString(text)
			s.emit(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": s.reasoningID,
				"output_index": len(s.outputs), "summary_index": 0, "delta": text})
		}
	case "reasoning-end":
		s.closeReasoning()
	case "text-delta":
		if text := event.Get("text").String(); text != "" {
			s.openMessage()
			s.message.WriteString(text)
			s.emit(map[string]any{"type": "response.output_text.delta", "item_id": s.messageID,
				"output_index": len(s.outputs), "content_index": 0, "delta": text})
		}
	case "text-end":
		s.closeMessage()
	case "tool-call":
		s.toolCall(event)
	case "finish":
		s.finish(event)
	}
}

func (s *commandCodeStream) finish(event gjson.Result) {
	s.start()
	s.closeReasoning()
	s.closeMessage()
	reason := strings.ToLower(event.Get("rawFinishReason").String() + " " + event.Get("finishReason").String())
	status := "completed"
	response := s.responseObject(status)
	switch {
	case strings.Contains(reason, "refusal") || strings.Contains(reason, "content") || strings.Contains(reason, "safety"):
		status = "incomplete"
		response["incomplete_details"] = map[string]any{"reason": "content_filter"}
	case strings.Contains(reason, "length") || strings.Contains(reason, "max_tokens"):
		status = "incomplete"
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	response["status"] = status
	response["output"] = s.outputs
	if usage := event.Get("totalUsage"); usage.IsObject() {
		input := usage.Get("inputTokens").Int()
		output := usage.Get("outputTokens").Int()
		response["usage"] = map[string]any{
			"input_tokens":          input,
			"input_tokens_details":  map[string]any{"cached_tokens": usage.Get("inputTokenDetails.cacheReadTokens").Int()},
			"output_tokens":         output,
			"output_tokens_details": map[string]any{"reasoning_tokens": usage.Get("outputTokenDetails.reasoningTokens").Int()},
			"total_tokens":          input + output,
		}
	}
	kind := "response.completed"
	if status == "incomplete" {
		kind = "response.incomplete"
	}
	s.emit(map[string]any{"type": kind, "response": response})
	s.finished = true
}

// fail reports an error event of the gateway's stream as a Responses error
// event. The gateway sends it after the request was accepted, so the HTTP
// status of the response was 200.
func (s *commandCodeStream) fail(event gjson.Result) {
	message := event.Get("error.message").String()
	if message == "" {
		message = event.Get("error").String()
	}
	if message == "" {
		message = event.Get("message").String()
	}
	if message == "" {
		message = "Stream error"
	}
	code := int(event.Get("error.statusCode").Int())
	if code < 400 || code > 599 {
		code = 502
	}
	s.emit(map[string]any{"type": "error", "error": map[string]any{
		"code": code, "message": message, "is_retryable": event.Get("error.isRetryable").Bool(),
	}})
	s.finished = true
}
