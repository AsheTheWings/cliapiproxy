//go:build component

package snapshots

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const deadline = 5 * time.Second

// fetcherFunc adapts a function to a Fetcher.
type fetcherFunc func(ctx context.Context) (Reading, error)

func (f fetcherFunc) Fetch(ctx context.Context, _ *http.Client, _ *cliproxyauth.Auth) (Reading, error) {
	return f(ctx)
}

// recordingWriter counts the snapshots it is asked to store.
type recordingWriter struct{ writes atomic.Int64 }

func (w *recordingWriter) WriteSnapshot(context.Context, Snapshot) (int64, error) {
	return w.writes.Add(1), nil
}

func credential(provider string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: provider + "-1", Provider: provider, Attributes: map[string]string{"api_key": "synthetic-key"},
		Metadata: map[string]any{"access_token": "synthetic-token", "dca_token": "dca:synthetic", "project_id": "synthetic-project"}}
}

func reading() Reading {
	return Reading{Plan: "pro", Meters: []Meter{{Meter: "weekly", Unit: UnitPercent, Used: ptr(10), WindowSeconds: secondsPerWeek}}}
}

func newPoller(auth *cliproxyauth.Auth, f Fetcher, w Writer, timeout time.Duration) *Poller {
	return NewPoller(PollerOptions{
		Credentials: func() []*cliproxyauth.Auth { return []*cliproxyauth.Auth{auth} },
		Client:      func(context.Context, *cliproxyauth.Auth) *http.Client { return &http.Client{} },
		Writer:      w,
		Fetchers:    map[string]Fetcher{auth.Provider: f},
		Timeout:     timeout,
	})
}

func TestOneCredentialNeverPollsTwiceAtOnce(t *testing.T) {
	release := make(chan struct{})
	var running, most, calls atomic.Int64
	f := fetcherFunc(func(context.Context) (Reading, error) {
		calls.Add(1)
		if n := running.Add(1); n > most.Load() {
			most.Store(n)
		}
		defer running.Add(-1)
		<-release
		return reading(), nil
	})
	auth := credential("claude")
	p := newPoller(auth, f, &recordingWriter{}, deadline)
	ctx := context.Background()
	p.PollDue(ctx)
	wait.Until(t, deadline, func() bool { return running.Load() == 1 }, "the first poll never started")
	// The credential is due again, by request and by baseline, while its
	// poll runs.
	p.Touch(auth.ID)
	for range 5 {
		p.PollDue(ctx)
	}
	close(release)
	p.wg.Wait()
	if calls.Load() != 1 || most.Load() != 1 {
		t.Fatalf("calls = %d, most at once = %d, want 1 and 1", calls.Load(), most.Load())
	}
}

func TestPollPastItsTimeoutIsAbandoned(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	w := &recordingWriter{}
	// The fetcher ignores its context, as a hung connection would.
	p := newPoller(credential("codex"), fetcherFunc(func(context.Context) (Reading, error) {
		<-stuck
		return reading(), nil
	}), w, 50*time.Millisecond)
	done := make(chan string, 1)
	go func() { done <- p.Poll(context.Background(), credential("codex")) }()
	if outcome := wait.Receive(t, done, deadline, "the poll was not abandoned at its timeout"); outcome != OutcomeTimeout {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeTimeout)
	}
	if w.writes.Load() != 0 {
		t.Fatal("an abandoned poll wrote a snapshot")
	}
}

func TestFailedAndUnparseablePollsWriteNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		outcome string
	}{
		"http error":  {&HTTPError{Status: http.StatusUnauthorized}, OutcomeHTTPError},
		"unparseable": {ErrUnparseable, OutcomeUnparseable},
	} {
		w := &recordingWriter{}
		p := newPoller(credential("kimi"), fetcherFunc(func(context.Context) (Reading, error) { return Reading{}, tc.err }), w, deadline)
		if outcome := p.Poll(context.Background(), credential("kimi")); outcome != tc.outcome || w.writes.Load() != 0 {
			t.Errorf("%s: outcome = %q with %d writes, want %q with none", name, outcome, w.writes.Load(), tc.outcome)
		}
	}
}

// fixedURLs are each fetcher's provider URLs, without their query.
var fixedURLs = map[string][]string{
	"kiro":        {"https://management.us-east-1.kiro.dev/"},
	"commandcode": {"https://api.commandcode.ai/alpha/whoami", "https://api.commandcode.ai/alpha/billing/subscriptions", "https://api.commandcode.ai/alpha/billing/credits"},
	"claude":      {claudeUsageURL},
	"codex":       {codexUsageURL},
	"kimi":        {"https://api.kimi.com/coding/v1/usages"},
	"kimi-ai":     {"https://api.kimi.ai/coding/v1/usages"},
	"xai":         {"https://cli-chat-proxy.grok.com/v1/billing", "https://cli-chat-proxy.grok.com/v1/user"},
	"meta":        {metaQuotaURL},
	"devin":       {devinUserStatusURL},
	"antigravity": {antigravityQuotaURL},
}

// recordingTransport answers every request with an empty object and keeps
// its URL.
type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
	rt.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
}

func TestFetchersIgnoreAHostileBaseURL(t *testing.T) {
	if len(fixedURLs) != len(Fetchers) {
		t.Fatalf("the test lists %d fetchers' URLs; the server has %d fetchers", len(fixedURLs), len(Fetchers))
	}
	const hostile = "http://attacker.invalid/steal"
	for provider, fetcher := range Fetchers {
		auth := credential(provider)
		for _, name := range []string{"base_url", "base-url", "management_url", "api_url", "url", "endpoint"} {
			auth.Attributes[name] = hostile
			auth.Metadata[name] = hostile
		}
		rt := &recordingTransport{}
		_, _ = fetcher.Fetch(context.Background(), &http.Client{Transport: rt}, auth)
		if len(rt.urls) == 0 {
			t.Errorf("%s sent no request", provider)
		}
		for _, u := range rt.urls {
			fixed := false
			for _, allowed := range fixedURLs[provider] {
				fixed = fixed || u == allowed
			}
			if !fixed {
				t.Errorf("%s requested %s, outside its fixed URLs", provider, u)
			}
		}
	}
}

func TestPollSignals(t *testing.T) {
	rec := telemetrytest.Install(t)
	ok := newPoller(credential("claude"), fetcherFunc(func(context.Context) (Reading, error) { return reading(), nil }), &recordingWriter{}, deadline)
	failed := newPoller(credential("codex"), fetcherFunc(func(context.Context) (Reading, error) {
		return Reading{}, &HTTPError{Status: http.StatusTooManyRequests}
	}), &recordingWriter{}, deadline)
	ok.Poll(context.Background(), credential("claude"))
	failed.Poll(context.Background(), credential("codex"))

	spans := map[string]string{}
	for _, span := range rec.Spans("cliapiproxy.usage.poll") {
		provider, _ := telemetrytest.Attr(span, "cliapiproxy.provider")
		outcome, _ := telemetrytest.Attr(span, "cliapiproxy.usage.poll_outcome")
		spans[provider.AsString()] = outcome.AsString()
	}
	durations := map[string]string{}
	for _, m := range rec.Metrics(t) {
		if h, isHistogram := m.Data.(metricdata.Histogram[float64]); isHistogram && m.Name == "cliapiproxy.usage.poll.duration" {
			for _, p := range h.DataPoints {
				provider, _ := p.Attributes.Value("cliapiproxy.provider")
				outcome, _ := p.Attributes.Value("cliapiproxy.usage.poll_outcome")
				durations[provider.AsString()] = outcome.AsString()
			}
		}
	}
	want := map[string]string{"claude": OutcomeOK, "codex": OutcomeHTTPError}
	for provider, outcome := range want {
		if spans[provider] != outcome || durations[provider] != outcome {
			t.Errorf("%s: span outcome %q, duration outcome %q, want %q", provider, spans[provider], durations[provider], outcome)
		}
	}
	rec.CheckCatalog(t)
}
