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

func (s sink) Submit(g capture.Generation)      { s <- g }
func (s sink) PayloadLimit() int                { return 1 << 20 }
func (s sink) ClientKeyLabel(key string) string { return "laptop" }

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
