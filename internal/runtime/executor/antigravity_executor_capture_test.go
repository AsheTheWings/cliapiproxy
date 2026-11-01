//go:build component

package executor

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

type generationSink chan capture.Generation

func (s generationSink) Reserve() func(capture.Generation) {
	return func(g capture.Generation) { s <- g }
}
func (s generationSink) PayloadLimit() int { return 1 << 20 }
func (s generationSink) ClientKeyLabel(string) string {
	return ""
}

func TestAntigravityReportsItsUpstreamBodyWithRequestLoggingOff(t *testing.T) {
	submitted := make(generationSink, 1)
	capture.SetSink(submitted)
	t.Cleanup(func() { capture.SetSink(nil) })
	ctx, c := capture.Begin(context.Background(), []byte(`{"model":"gemini-2.5-pro"}`), "application/json", "", false)

	executor := &AntigravityExecutor{cfg: &config.Config{}}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	req, err := executor.buildRequest(ctx, auth, "token", "gemini-2.5-pro", []byte(`{"request":{"contents":[]}}`), false, "", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	sent, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	capture.UpstreamResponse(ctx, []byte(`{"response":{}}`))
	capture.Attempt(ctx, usage.Record{RequestedAt: time.Now()}, capture.Credential{Provider: "antigravity"})
	c.Finish(ctx, nil)

	g := wait.Receive(t, submitted, 5*time.Second, "no generation was submitted")
	for _, p := range g.Payloads {
		if p.Kind == capture.KindUpstreamInput && (len(p.Body) == 0 || !bytes.Equal(p.Body, sent)) {
			t.Fatalf("upstream_input = %q, want the %d bytes sent", p.Body, len(sent))
		}
	}
}
