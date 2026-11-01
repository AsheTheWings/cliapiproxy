//go:build component

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations"
	"github.com/google/uuid"
)

// countingStore answers every store read with an empty result and counts
// the reads.
type countingStore struct{ reads atomic.Int64 }

func (s *countingStore) Quota(context.Context) ([]generations.CredentialUsage, error) {
	s.reads.Add(1)
	return []generations.CredentialUsage{}, nil
}

func (s *countingStore) RefreshUsage(_ context.Context, id string) (string, generations.CredentialUsage, error) {
	s.reads.Add(1)
	return "unchanged", generations.CredentialUsage{CredentialID: id}, nil
}

func (s *countingStore) Timeline(context.Context, string, time.Time, time.Time) ([]generations.TimelinePoint, error) {
	s.reads.Add(1)
	return []generations.TimelinePoint{}, nil
}

func (s *countingStore) Generations(context.Context, generations.GenerationFilter) (generations.GenerationPage, error) {
	s.reads.Add(1)
	return generations.GenerationPage{Generations: []generations.Generation{
		{TraceID: "0123456789abcdef0123456789abcdef"}, {}}}, nil
}

func (s *countingStore) Generation(_ context.Context, id uuid.UUID) (generations.GenerationDetail, error) {
	s.reads.Add(1)
	return generations.GenerationDetail{Generation: generations.Generation{ID: id}}, nil
}

func (s *countingStore) Payload(context.Context, uuid.UUID, string) (generations.PayloadBody, error) {
	s.reads.Add(1)
	return generations.PayloadBody{Body: []byte(`{}`), ContentType: "application/json", Rebuilt: true}, nil
}

func (s *countingStore) Summary(context.Context, generations.SummaryQuery) ([]generations.Bucket, error) {
	s.reads.Add(1)
	return []generations.Bucket{}, nil
}

func (s *countingStore) Latency(context.Context, time.Time, time.Time) ([]generations.Latency, error) {
	s.reads.Add(1)
	return []generations.Latency{}, nil
}

// storeRoutes are the quota and generations routes with valid requests.
var storeRoutes = []struct{ method, target, body string }{
	{http.MethodGet, "/v8/management/quota", ""},
	{http.MethodPost, "/v8/management/quota/refresh", `{"credential_id":"kiro-1"}`},
	{http.MethodGet, "/v8/management/quota/timeline?credential=kiro-1", ""},
	{http.MethodGet, "/v8/management/generations?provider=codex&limit=10", ""},
	{http.MethodGet, "/v8/management/generations/summary?group_by=credential,client", ""},
	{http.MethodGet, "/v8/management/generations/latency?from=2026-10-01&to=2026-10-31", ""},
	{http.MethodGet, "/v8/management/generations/" + uuid.NewString(), ""},
	{http.MethodGet, "/v8/management/generations/" + uuid.NewString() + "/payloads/client_input", ""},
}

func serveStoreRoute(s *Server, method, target, body, remote, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	return response
}

func TestStoreRoutesRequireTheManagementKey(t *testing.T) {
	for _, route := range storeRoutes {
		// Each route gets its own server, so its two failed attempts stay
		// below the block that five failures from one address set.
		s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
		store := &countingStore{}
		s.mgmt.SetStore(store, "")
		for _, key := range []string{"", "wrong-password"} {
			got := serveStoreRoute(s, route.method, route.target, route.body, "127.0.0.1:1234", key)
			if got.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with key %q: status %d, want 401", route.method, route.target, key, got.Code)
			}
		}
		if store.reads.Load() != 0 {
			t.Fatalf("%s %s read the store for a rejected request", route.method, route.target)
		}
		got := serveStoreRoute(s, route.method, route.target, route.body, "127.0.0.1:1234", "test-password")
		if got.Code != http.StatusOK || store.reads.Load() != 1 {
			t.Fatalf("%s %s with the key: status %d, body %s", route.method, route.target, got.Code, got.Body)
		}
	}
}

func TestStoreRoutesAnswer503WithoutTheStore(t *testing.T) {
	s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
	for _, route := range storeRoutes {
		got := serveStoreRoute(s, route.method, route.target, route.body, "127.0.0.1:1234", "test-password")
		if got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "the store is not configured") {
			t.Fatalf("%s %s without the store: status %d, body %s", route.method, route.target, got.Code, got.Body)
		}
	}
}

func TestPayloadRouteMarksARebuiltBody(t *testing.T) {
	s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
	s.mgmt.SetStore(&countingStore{}, "")
	got := serveStoreRoute(s, http.MethodGet, "/v8/management/generations/"+uuid.NewString()+"/payloads/upstream_input", "", "127.0.0.1:1234", "test-password")
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "application/json" || got.Header().Get("X-Payload-Rebuilt") != "true" ||
		got.Header().Get("X-Content-Type-Options") != "nosniff" || got.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("payload: status %d, headers %v", got.Code, got.Header())
	}
}

func TestGenerationsLinkTheirTracesOnlyWithATemplate(t *testing.T) {
	for _, tc := range []struct{ template, want string }{
		{"https://traces.example/trace/{trace_id}?view=1", `"trace_url":"https://traces.example/trace/0123456789abcdef0123456789abcdef?view=1"`},
		{"", ""},
	} {
		s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
		s.mgmt.SetStore(&countingStore{}, tc.template)
		body := serveStoreRoute(s, http.MethodGet, "/v8/management/generations", "", "127.0.0.1:1234", "test-password").Body.String()
		if links := strings.Count(body, `"trace_url"`); tc.want == "" && links != 0 || tc.want != "" && (links != 1 || !strings.Contains(body, tc.want)) {
			t.Fatalf("template %q: body %s", tc.template, body)
		}
	}
}
