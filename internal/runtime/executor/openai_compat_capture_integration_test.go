//go:build integration

package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	sdktranslator "github.com/AsheTheWings/cliapiproxy/sdk/translator"
)

type terminalGenerationSink chan capture.Generation

func (s terminalGenerationSink) Reserve() func(capture.Generation) {
	return func(g capture.Generation) { s <- g }
}

func TestImageTransportCapturePreservesSplitJSONAndItsFacts(t *testing.T) {
	parts := []string{
		"event: image_generation.completed\r\ndata: {\"type\":\"image_generation.comp",
		"leted\",\"model\":\"dall-e-3\",\"usage\":{\"kiro:cre",
		"dits\":{\"value\":0.2}}}\r\n\r\n",
	}
	advance := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i, part := range parts {
			_, _ = io.WriteString(w, part)
			w.(http.Flusher).Flush()
			if i < len(parts)-1 {
				select {
				case <-advance:
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	submitted := make(terminalGenerationSink, 1)
	capture.SetSink(submitted)
	t.Cleanup(func() { capture.SetSink(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	body := []byte(`{"model":"dall-e-3","prompt":"synthetic image"}`)
	ctx, c := capture.Begin(ctx, body, "application/json", "", true)
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-image-key"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "dall-e-3", Payload: body}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var forwarded bytes.Buffer
	boundaries := []int{len(parts[0]), len(parts[0]) + len(parts[1])}
	acked, chunks := 0, 0
	for chunk := range c.Stream(ctx, result).Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		chunks++
		forwarded.Write(chunk.Payload)
		for acked < len(boundaries) && forwarded.Len() >= boundaries[acked] {
			select {
			case advance <- struct{}{}:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			acked++
		}
	}
	want := strings.Join(parts, "")
	if chunks < 3 || forwarded.String() != want {
		t.Fatal("the real executor did not forward the forced transport boundaries verbatim")
	}
	g := wait.Receive(t, submitted, 5*time.Second, "split image terminal was not captured")
	if g.Status != capture.StatusCompleted || g.UsageAmount != "0.2" || g.UsageUnit != "credits" {
		t.Fatalf("split terminal facts = %s, %s %s", g.Status, g.UsageAmount, g.UsageUnit)
	}
	found := false
	for _, p := range g.Payloads {
		if p.Kind != capture.KindUpstreamResponse {
			continue
		}
		found = true
		sum := sha256.Sum256([]byte(want))
		if string(p.Body) != want || p.OriginalBytes != int64(len(want)) || !bytes.Equal(p.SHA256, sum[:]) {
			t.Fatal("raw transport capture changed response bytes, size, or SHA-256")
		}
	}
	if !found {
		t.Fatal("upstream response payload is missing")
	}
}
func (s terminalGenerationSink) PayloadLimit() int            { return 1 << 20 }
func (s terminalGenerationSink) ClientKeyLabel(string) string { return "" }

func TestOpenAICompatCaptureRequiresAnUpstreamTerminalSignal(t *testing.T) {
	for _, tc := range []struct{ name, terminal, status string }{
		{"EOF", "", ""},
		{"done", "data: [DONE]\n\n", capture.StatusCompleted},
		{"stop", "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n", capture.StatusCompleted},
		{"length", "data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\n", capture.StatusIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"+tc.terminal)
			}))
			t.Cleanup(server.Close)
			submitted := make(terminalGenerationSink, 1)
			capture.SetSink(submitted)
			t.Cleanup(func() { capture.SetSink(nil) })
			body := []byte(`{"model":"capture-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			ctx, c := capture.Begin(ctx, body, "application/json", "", true)
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "synthetic-capture-key"}}
			result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "capture-model", Payload: body}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range c.Stream(ctx, result).Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
			if tc.status == "" {
				if len(submitted) != 0 {
					t.Fatal("clean EOF without a terminal signal stored a generation")
				}
				return
			}
			g := wait.Receive(t, submitted, 5*time.Second, "terminal response did not store a generation")
			if g.Status != tc.status {
				t.Fatalf("status = %s, want %s", g.Status, tc.status)
			}
		})
	}
}
