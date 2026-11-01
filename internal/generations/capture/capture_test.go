//go:build component

package capture_test

import (
	"context"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/fakeexec"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

// sink keeps submitted generations in memory.
type sink chan capture.Generation

func (s sink) Reserve() func(capture.Generation) { return func(g capture.Generation) { s <- g } }
func (s sink) PayloadLimit() int                 { return 1 << 20 }
func (s sink) ClientKeyLabel(key string) string  { return "laptop" }

func TestGenerationCarriesTheExecuteSpansTraceID(t *testing.T) {
	rec := telemetrytest.Install(t)
	submitted := make(sink, 1)
	capture.SetSink(submitted)
	t.Cleanup(func() { capture.SetSink(nil) })
	manager := fakeexec.Manager(t, &fakeexec.Executor{}, &cliproxyauth.Auth{ID: "fake-trace", Provider: fakeexec.Provider, Status: cliproxyauth.StatusActive})

	ctx := usage.WithAPIKey(context.Background(), "synthetic-client-key")
	if _, err := manager.Execute(ctx, []string{fakeexec.Provider}, cliproxyexecutor.Request{Model: fakeexec.ModelComplete, Payload: []byte(`{"input":"ping"}`)},
		cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	g := wait.Receive(t, submitted, 5*time.Second, "no generation was submitted")
	span := rec.Span(t, "cliapiproxy.execute")
	if want := span.SpanContext().TraceID().String(); g.TraceID != want || len(g.TraceID) != 32 {
		t.Fatalf("trace_id = %q, want the execute span's %q", g.TraceID, want)
	}
	if g.ClientKeyLabel != "laptop" || len(g.ClientKeyFingerprint) != 12 {
		t.Fatalf("client = %q %q, want the label and a fingerprint", g.ClientKeyLabel, g.ClientKeyFingerprint)
	}
}

// acknowledgedStream runs a stream whose consumer cancels the execution
// after resolving delivery with deliveryErr, as the Responses handler does
// once it wrote response.completed, while the producer, like Antigravity's,
// closes its channel only after that cancellation and then reports success.
// It returns once the stream ended and the producer reported its attempt.
func acknowledgedStream(t *testing.T, deliveryErr error) sink {
	t.Helper()
	submitted := make(sink, 1)
	capture.SetSink(submitted)
	t.Cleanup(func() { capture.SetSink(nil) })
	deliveryCtx, finishDelivery := usage.WithStreamDelivery(context.Background())
	ctx, cancel := context.WithCancel(deliveryCtx)
	defer cancel()
	ctx, c := capture.Begin(ctx, []byte(`{"stream":true}`), "application/json", "", true)
	capture.UpstreamRequest(ctx, []byte(`{"input":"ping"}`), "application/json")
	capture.UpstreamResponseType(ctx, "text/event-stream")

	in := make(chan cliproxyexecutor.StreamChunk)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		frame := []byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":3}}}`)
		capture.UpstreamResponse(ctx, frame)
		in <- cliproxyexecutor.StreamChunk{Payload: frame}
		<-ctx.Done()
		close(in)
		capture.Attempt(ctx, usage.Record{RequestedAt: time.Now()}, capture.Credential{Provider: "antigravity"})
	}()
	out := c.Stream(ctx, &cliproxyexecutor.StreamResult{Chunks: in})
	<-out.Chunks
	finishDelivery(deliveryErr)
	cancel()
	for range out.Chunks {
	}
	wait.Receive(t, reported, 5*time.Second, "the producer never reported its attempt")
	return submitted
}

func TestAcknowledgedDeliveryCompletesACanceledStream(t *testing.T) {
	submitted := acknowledgedStream(t, nil)
	g := wait.Receive(t, submitted, 5*time.Second, "a generation whose terminal event was delivered was not submitted")
	if g.Status != capture.StatusCompleted {
		t.Fatalf("status = %q, want %q", g.Status, capture.StatusCompleted)
	}
}

func TestFailedDeliveryStoresNothing(t *testing.T) {
	submitted := acknowledgedStream(t, context.Canceled)
	// A capture that ended unsuccessfully submits nothing, even when its
	// attempt's record arrives after the end.
	if len(submitted) > 0 {
		t.Fatal("a stream whose delivery failed was submitted")
	}
}
