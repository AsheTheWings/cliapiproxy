//go:build component

package capture_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

//go:embed testdata/events.json
var eventFixture []byte

type limitedSink struct {
	sink
	limit int
}

func (s limitedSink) PayloadLimit() int { return s.limit }

func scanCapture(t *testing.T, client, response string, limit int, baseURL string, fragments ...bool) capture.Generation {
	t.Helper()
	submitted := make(sink, 1)
	capture.SetSink(limitedSink{submitted, limit})
	t.Cleanup(func() { capture.SetSink(nil) })
	ctx, c := capture.Begin(context.Background(), []byte(client), "application/json", "", true)
	cfg := &config.Config{}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{Body: []byte(client)})
	helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}})
	if len(fragments) > 0 && fragments[0] {
		for _, b := range []byte(response) {
			helps.AppendAPIResponseFragment(ctx, cfg, []byte{b})
		}
	} else {
		scanner := bufio.NewScanner(strings.NewReader(response))
		for scanner.Scan() {
			helps.AppendAPIResponseChunk(ctx, cfg, scanner.Bytes())
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	capture.Attempt(ctx, usage.Record{RequestedAt: time.Now(), BaseURL: baseURL}, capture.Credential{})
	c.Finish(ctx, nil)
	return wait.Receive(t, submitted, 5*time.Second, "scanner hooks did not submit the terminal generation")
}

func TestFragmentedTransportFactsSurviveThePayloadLimit(t *testing.T) {
	client := `{"padding":"` + strings.Repeat("x", 1200) + `","instructions":"late instruction"}`
	response := "data: {\"delta\":\"" + strings.Repeat("x", 1200) + "\"}\r\n\r\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"kiro:credits\":{\"value\":0.2}}}}\r\n\r\n"
	g := scanCapture(t, client, response, 1024, "", true)
	if g.Status != capture.StatusIncomplete || g.SystemInstruction != "late instruction" || g.UsageAmount != "0.2" || g.UsageUnit != "credits" {
		t.Fatal("fragmented terminal facts beyond the payload cutoff were lost")
	}
	for _, p := range g.Payloads {
		if !p.Truncated || len(p.Body) != 1024 {
			t.Fatal("a fragmented payload exceeded its byte limit")
		}
		if p.Kind == capture.KindUpstreamResponse {
			sum := sha256.Sum256([]byte(response))
			if string(p.Body) != response[:1024] || p.OriginalBytes != int64(len(response)) || !bytes.Equal(p.SHA256, sum[:]) {
				t.Fatal("fragmented payload bytes or full-body facts changed")
			}
		}
	}
}

func TestScannerHooksPreserveThePayloadViewEventFixture(t *testing.T) {
	var fixture struct{ Body string }
	if err := json.Unmarshal(eventFixture, &fixture); err != nil {
		t.Fatal(err)
	}
	g := scanCapture(t, `{}`, fixture.Body, 1<<20, "")
	for _, p := range g.Payloads {
		if p.Kind != capture.KindUpstreamResponse {
			continue
		}
		want := sha256.Sum256([]byte(fixture.Body))
		if string(p.Body) != fixture.Body || p.OriginalBytes != int64(len(fixture.Body)) || !bytes.Equal(p.SHA256, want[:]) {
			t.Fatal("scanner-hook response differs from the fixture used by the dashboard payload view")
		}
	}
}

func TestFactsBeyondThePayloadCutoffAreRetained(t *testing.T) {
	client := `{"padding":"` + strings.Repeat("x", 1200) + `","instructions":"keep\u0000this"}`
	for _, tc := range []struct{ name, terminal, baseURL, amount, unit string }{
		{"kiro", `{"type":"response.incomplete","response":{"usage":{"kiro:credits":{"value":0.2}}}}`, "", "0.2", "credits"},
		{"commandcode", `{"type":"finish","finishReason":"length","totalUsage":{"inputTokens":5}}`, "", "0.03", "usd"},
		{"openrouter", `{"choices":[{"finish_reason":"length"}],"usage":{"cost":0.04}}`, "https://openrouter.ai/api/v1", "0.04", "credits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 1200) + "\"}\n\n"
			if tc.name == "commandcode" {
				response += "data: {\"type\":\"provider-metadata\",\"providerMetadata\":{\"gateway\":{\"cost\":\"0.03\"}}}\n\n"
			}
			response += "data: " + tc.terminal + "\n\n"
			g := scanCapture(t, client, response, 1024, tc.baseURL)
			if g.Status != capture.StatusIncomplete || g.SystemInstruction != "keep\x00this" || g.UsageAmount != tc.amount || g.UsageUnit != tc.unit {
				t.Fatalf("facts = status %s, instruction %q, cost %q %q", g.Status, g.SystemInstruction, g.UsageAmount, g.UsageUnit)
			}
			if len(g.UsageRaw) == 0 {
				t.Fatal("usage beyond the payload cutoff was discarded")
			}
			for _, p := range g.Payloads {
				if !p.Truncated || len(p.Body) != 1024 {
					t.Fatalf("%s: payload is not bounded and truncated", p.Kind)
				}
			}
		})
	}
}
