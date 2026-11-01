// Package fakeexec is a provider executor for tests of what the server does
// around an upstream call. It reports its upstream bodies and usage through
// the same helpers real executors use, and its behavior follows the model a
// request names.
package fakeexec

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/registry"
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

// Provider is the executor's provider key.
const Provider = "fakeprov"

// Models select a behavior.
const (
	// ModelComplete streams Chunks and completes.
	ModelComplete = "fake-complete"
	// ModelIncomplete streams a Responses response.incomplete event.
	ModelIncomplete = "fake-incomplete"
	// ModelFail fails the stream after its first chunk.
	ModelFail = "fake-fail"
	// ModelHold streams one chunk, then waits for the caller to go away.
	ModelHold = "fake-hold"
	// ModelWebsocket streams Chunks as WebSocket frames.
	ModelWebsocket = "fake-websocket"
)

// Models lists every model the executor serves.
var Models = []string{ModelComplete, ModelIncomplete, ModelFail, ModelHold, ModelWebsocket}

// Chunks is the stream a completing request returns.
var Chunks = [][]byte{
	[]byte(`data: {"type":"response.output_text.delta","delta":"pong"}`),
	[]byte(`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`),
}

var incompleteChunks = [][]byte{
	[]byte(`data: {"type":"response.output_text.delta","delta":"po"}`),
	[]byte(`data: {"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":12,"output_tokens":1,"total_tokens":13}}}`),
}

// Executor serves Provider. UpstreamBody, when set, builds the upstream
// body from the client payload and the credential.
type Executor struct {
	UpstreamBody func(payload []byte, auth *cliproxyauth.Auth) []byte
}

func (e *Executor) Identifier() string { return Provider }

func (e *Executor) upstream(req cliproxyexecutor.Request, auth *cliproxyauth.Auth) []byte {
	if e.UpstreamBody != nil {
		return e.UpstreamBody(req.Payload, auth)
	}
	return req.Payload
}

var errUpstream = errors.New("upstream failed")

// Execute returns the completed response's final event as one body.
func (e *Executor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	reporter := helps.NewUsageReporter(ctx, Provider, req.Model, auth)
	helps.RecordAPIRequest(ctx, nil, helps.UpstreamRequestLog{Body: e.upstream(req, auth)})
	body := []byte(`{"object":"response","status":"completed","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`)
	helps.AppendAPIResponseChunk(ctx, nil, body)
	reporter.Publish(ctx, usage.Detail{InputTokens: 12, OutputTokens: 3, TotalTokens: 15})
	return cliproxyexecutor.Response{Payload: body}, nil
}

// ExecuteStream streams the behavior req.Model names.
func (e *Executor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	reporter := helps.NewUsageReporter(ctx, Provider, req.Model, auth)
	websocket := req.Model == ModelWebsocket
	if websocket {
		helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{Body: e.upstream(req, auth)})
	} else {
		helps.RecordAPIRequest(ctx, nil, helps.UpstreamRequestLog{Body: e.upstream(req, auth), Headers: http.Header{"Content-Type": {"application/json"}}})
		helps.RecordAPIResponseMetadata(ctx, nil, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}})
	}
	chunks := Chunks
	if req.Model == ModelIncomplete {
		chunks = incompleteChunks
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for i, chunk := range chunks {
			if websocket {
				helps.AppendAPIWebsocketResponse(ctx, nil, chunk[len("data: "):])
			} else {
				helps.AppendAPIResponseChunk(ctx, nil, chunk)
			}
			if !send(cliproxyexecutor.StreamChunk{Payload: chunk}) {
				reporter.PublishFailure(ctx, ctx.Err())
				return
			}
			if i == 0 && req.Model == ModelFail {
				reporter.PublishFailure(ctx, errUpstream)
				send(cliproxyexecutor.StreamChunk{Err: errUpstream})
				return
			}
			if i == 0 && req.Model == ModelHold {
				<-ctx.Done()
				reporter.PublishFailure(ctx, ctx.Err())
				return
			}
		}
		reporter.Publish(ctx, usage.Detail{InputTokens: 12, OutputTokens: 3, TotalTokens: 15})
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}

func (e *Executor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *Executor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errUpstream
}

func (e *Executor) HttpRequest(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errUpstream
}

// Manager returns an auth manager whose only credential is auth, served by
// e, with Models registered for it until the test ends.
func Manager(t testing.TB, e *Executor, auth *cliproxyauth.Auth) *cliproxyauth.Manager {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(e)
	var models []*registry.ModelInfo
	for _, m := range Models {
		models = append(models, &registry.ModelInfo{ID: m})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, Provider, models)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	return manager
}
