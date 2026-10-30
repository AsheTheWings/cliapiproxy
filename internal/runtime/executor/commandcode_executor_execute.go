package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

type commandCodePreparedRequest struct {
	applyPatch      *helps.ApplyPatchResponsesState
	baseModel       string
	upstreamModel   string
	from            sdktranslator.Format
	responseFormat  sdktranslator.Format
	to              sdktranslator.Format
	originalPayload []byte
	// body is the request in Responses format that the response translators
	// read; upstreamBody is what the gateway receives.
	body         []byte
	upstreamBody []byte
	threadID     string
	apiKey       string
}

func (e *CommandCodeExecutor) prepareRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*commandCodePreparedRequest, error) {
	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "commandcode executor: the credential has no API key"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := commandcodeauth.UpstreamModel(baseModel)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")

	originalSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalSource)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, upstreamModel, originalPayload, stream, false)
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", upstreamModel)
	body, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, from, to, upstreamModel, bytes.Clone(req.Payload), stream, false)
	if err != nil {
		return nil, err
	}
	if thinking.ExtractReasoningEffort(req.Payload, from.String(), req.Model) == "" {
		// The request names no effort, so the effort the translator filled in is
		// dropped and the model's own default applies.
		body = commandCodeDropTranslatedEffort(body)
	}
	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)
	applyPatch := helps.NewApplyPatchResponsesState(from, originalPayload, originalTranslated)
	body, err = helps.NormalizeApplyPatchResponsesRequest(body, originalPayload)
	if err != nil {
		return nil, err
	}
	body = helps.NormalizeCodexToolSchemas(body)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, upstreamModel, e.Identifier(), from.String(), "", body, originalTranslated,
		helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)

	catalogModel, _ := commandCodeCatalogModel(auth, upstreamModel)
	built, err := buildCommandCodeRequest(commandCodeRequestInput{
		Body:         body,
		Model:        upstreamModel,
		SessionID:    util.SessionIDFromContext(ctx),
		CatalogModel: catalogModel,
		MaxTokens:    commandCodeRequestedMaxTokens(originalPayload),
	})
	if err != nil {
		return nil, err
	}
	prepared := &commandCodePreparedRequest{
		applyPatch:      applyPatch,
		baseModel:       baseModel,
		upstreamModel:   upstreamModel,
		from:            from,
		responseFormat:  responseFormat,
		to:              to,
		originalPayload: originalPayload,
		body:            body,
		upstreamBody:    built.Body,
		threadID:        built.ThreadID,
		apiKey:          apiKey,
	}
	return prepared, nil
}

// commandCodeRequestedMaxTokens returns the output limit a client request names
// in any of the supported API formats; the translated Responses body does not
// keep it.
func commandCodeRequestedMaxTokens(payload []byte) int64 {
	for _, path := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "generationConfig.maxOutputTokens", "request.generationConfig.maxOutputTokens"} {
		if value := gjson.GetBytes(payload, path).Int(); value > 0 {
			return value
		}
	}
	return 0
}

// send posts the prepared request. The gateway answers only as a stream, and the
// body of a successful response is converted to a Responses event stream.
func (e *CommandCodeExecutor) send(ctx context.Context, auth *cliproxyauth.Auth, prepared *commandCodePreparedRequest, reporter *helps.UsageReporter, roundTripOnly bool) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.generateURL, bytes.NewReader(prepared.upstreamBody))
	if err != nil {
		return nil, err
	}
	applyCommandCodeHeaders(httpReq, prepared.apiKey, prepared.threadID)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       e.generateURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      prepared.upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	if roundTripOnly {
		httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	} else {
		httpClient = reporter.TrackHTTPClient(httpClient)
	}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		httpResp.Body = newCommandCodeStream(httpResp.Body, prepared.upstreamModel)
	}
	return httpResp, nil
}

func (e *CommandCodeExecutor) failedResponseError(ctx context.Context, httpResp *http.Response) error {
	data, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("commandcode executor: close response body error: %v", errClose)
	}
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
	return wrapCommandCodeUpstreamError(httpResp.StatusCode, httpResp.Header, data)
}

// Execute runs a request and returns the complete response.
func (e *CommandCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeCompaction(ctx, auth, req, opts)
	}

	prepared, errPrepare := e.prepareRequest(ctx, auth, req, opts, true)
	if errPrepare != nil {
		return resp, errPrepare
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.upstreamModel)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	httpResp, errSend := e.send(ctx, auth, prepared, reporter, false)
	if errSend != nil {
		return resp, errSend
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, e.failedResponseError(ctx, httpResp)
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.ObserveResponseModel(data)

	var upstreamUsage helps.StreamUsageBuffer
	out, errCompleted := e.translateCompleted(ctx, req, prepared, data, &upstreamUsage)
	if errCompleted != nil {
		upstreamUsage.PublishFailure(ctx, reporter, errCompleted)
		return resp, errCompleted
	}
	if len(out.sourceEvent) > 0 {
		reporter.ObserveResponseModel(out.sourceEvent)
	}
	if detail, ok := helps.ParseCodexUsage(out.sourceEvent); ok {
		reporter.Publish(ctx, detail)
	} else {
		reporter.EnsurePublished(ctx)
	}
	payload := out.payload
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		payload = helps.EnsureResponsesUsageDetails(payload)
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: httpResp.Header.Clone()}, nil
}

type commandCodeCompletedTranslation struct {
	payload     []byte
	sourceEvent []byte
}

// translateCompleted reads a complete event stream and translates its terminal
// response.
func (e *CommandCodeExecutor) translateCompleted(ctx context.Context, req cliproxyexecutor.Request, prepared *commandCodePreparedRequest, data []byte, upstreamUsage *helps.StreamUsageBuffer) (commandCodeCompletedTranslation, error) {
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmedLine := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmedLine, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
		if errEvent := commandCodeStreamEventError(eventData); errEvent != nil {
			return commandCodeCompletedTranslation{}, errEvent
		}
		if detail, ok := helps.ParseCodexUsage(eventData); ok {
			upstreamUsage.Observe(detail, true)
		}
		events, errBridge := prepared.applyPatch.Transform(eventData)
		if errBridge != nil {
			return commandCodeCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		for _, event := range events {
			eventType := gjson.GetBytes(event, "type").String()
			switch {
			case eventType == "response.output_item.done":
				xaiCollectOutputItemDone(event, outputItemsByIndex, &outputItemsFallback)
			case isCommandCodeTerminalEvent(eventType):
				completed := patchCodexCompletedOutput(event, outputItemsByIndex, outputItemsFallback)
				var param any
				out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completed, &param)
				if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
					return commandCodeCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				}
				return commandCodeCompletedTranslation{payload: out, sourceEvent: completed}, nil
			}
		}
	}
	if errFinish := prepared.applyPatch.Finish(); errFinish != nil {
		return commandCodeCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	return commandCodeCompletedTranslation{}, commandCodeStreamEndedError()
}

// commandCodeStreamEndedError reports a response that ended without a terminal event.
func commandCodeStreamEndedError() error {
	return statusErr{code: http.StatusRequestTimeout, msg: "commandcode stream error: stream disconnected before response.completed or response.incomplete"}
}

func isCommandCodeTerminalEvent(eventType string) bool {
	return eventType == "response.completed" || eventType == "response.incomplete"
}

// commandCodeStreamEventError returns the error an error event of the stream
// carries.
func commandCodeStreamEventError(eventData []byte) error {
	if gjson.GetBytes(eventData, "type").String() != "error" {
		return nil
	}
	status := http.StatusBadGateway
	if code := int(gjson.GetBytes(eventData, "error.code").Int()); code >= 400 && code <= 599 {
		status = code
	}
	return wrapCommandCodeUpstreamError(status, nil, eventData)
}

// commandCodeDropTranslatedEffort removes the reasoning effort from a Responses body.
func commandCodeDropTranslatedEffort(body []byte) []byte {
	if updated, err := sjson.DeleteBytes(body, "reasoning.effort"); err == nil {
		return updated
	}
	return body
}

// ExecuteStream runs a request and streams the translated response.
func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeCompactionStream(ctx, auth, req, opts)
	}

	prepared, errPrepare := e.prepareRequest(ctx, auth, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.upstreamModel)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	httpResp, errSend := e.send(ctx, auth, prepared, reporter, true)
	if errSend != nil {
		return nil, errSend
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, e.failedResponseError(ctx, httpResp)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("commandcode executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		terminalDelivered := false
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		fail := func(errFail error) {
			helps.RecordAPIResponseError(ctx, e.cfg, errFail)
			reporter.PublishFailure(ctx, errFail)
			send(cliproxyexecutor.StreamChunk{Err: errFail})
		}
		emit := func(line []byte) bool {
			lines, errBridge := prepared.applyPatch.Stream(line)
			if errBridge != nil {
				streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			var chunks [][]byte
			for _, patched := range lines {
				chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, patched, &param, claudeInputTokens)...)
			}
			helps.RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, &streamUsage, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					return false
				}
			}
			if helps.StopApplyPatchStreamWithUsage(ctx, param, reporter, &streamUsage, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return false
			}
			if errBridge != nil {
				errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				streamUsage.PublishFailure(ctx, reporter, errBridge)
				send(cliproxyexecutor.StreamChunk{Err: errBridge})
				return false
			}
			return true
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmedLine := bytes.TrimSpace(line)
			if !bytes.HasPrefix(trimmedLine, dataTag) {
				if !emit(bytes.Clone(line)) {
					return
				}
				continue
			}
			eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
			eventType := gjson.GetBytes(eventData, "type").String()
			helps.ObserveResponsesTokenEvent(reporter, eventData)
			if errEvent := commandCodeStreamEventError(eventData); errEvent != nil {
				fail(errEvent)
				return
			}
			switch {
			case eventType == "response.output_item.done":
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case isCommandCodeTerminalEvent(eventType):
				if detail, ok := helps.ParseCodexUsage(eventData); ok {
					streamUsage.Observe(detail, true)
				}
				eventData = patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			}
			if !emit(append([]byte("data: "), eventData...)) {
				return
			}
			if isCommandCodeTerminalEvent(eventType) {
				terminalDelivered = true
			}
		}
		finishEvents, errFinish := prepared.applyPatch.FinishStream()
		if errFinish != nil {
			streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
		}
		for _, event := range finishEvents {
			for _, chunk := range helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, event, &param, claudeInputTokens) {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunk}) {
					return
				}
			}
		}
		if errFinish != nil {
			errFinish = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			streamUsage.PublishFailure(ctx, reporter, errFinish)
			send(cliproxyexecutor.StreamChunk{Err: errFinish})
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			if terminalDelivered && errors.Is(errScan, context.Canceled) {
				return
			}
			fail(errScan)
			return
		}
		if !terminalDelivered && ctx.Err() == nil {
			fail(commandCodeStreamEndedError())
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CommandCodeExecutor) executeCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  helps.PrepareAntigravityCompactionSummaryPayload(helps.ResponsesCompactionSourcePayload(req, opts), baseModel),
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return resp, errSummary
	}
	respPayload, errBuild := helps.BuildResponsesCompactionResponseFromSummary(baseModel, summaryResp.Payload)
	if errBuild != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: errBuild.Error()}
	}
	return cliproxyexecutor.Response{Payload: respPayload, Headers: summaryResp.Headers}, nil
}

func (e *CommandCodeExecutor) executeCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryResp, errSummary := e.executeCompaction(ctx, auth, req, opts)
	if errSummary != nil {
		return nil, errSummary
	}
	chunks, errChunks := helps.BuildResponsesCompactionStreamChunksFromResponse(baseModel, summaryResp.Payload)
	if errChunks != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: errChunks.Error()}
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

// CountTokens estimates the input tokens of a request locally; the gateway has
// no counting route.
func (e *CommandCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	prepared, errPrepare := e.prepareRequest(ctx, auth, req, opts, false)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	enc, errEnc := tokenizer.Get(tokenizer.O200kBase)
	if errEnc != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode executor: tokenizer init failed: %w", errEnc)
	}
	count, errCount := countCodexInputTokens(enc, prepared.body)
	if errCount != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode executor: token counting failed: %w", errCount)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, prepared.to, prepared.responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}
