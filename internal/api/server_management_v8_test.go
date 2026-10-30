//go:build component

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/api/handlers/management"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/gin-gonic/gin"
)

// newV8ManagementServer serves the management routes of a configuration
// parsed from a v8 document, without a configuration file, as the server runs.
func newV8ManagementServer(t *testing.T, document string) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := config.ParseConfigBytes([]byte("config-version: 8\n" + document))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = t.TempDir()
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: management.NewHandler(cfg, "", nil)}
	if cfg.RemoteManagement.SecretKey != "" {
		s.managementRoutesEnabled.Store(true)
		s.registerManagementRoutes()
	}
	return s
}

func serveManagement(s *Server, method, target, remote, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	return response
}

// The management API is exactly these routes; the only one that writes
// configuration replaces the provider groups the server keeps as state.
func TestManagementRoutesAreTheExpectedSet(t *testing.T) {
	s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
	var got []string
	for _, route := range s.engine.Routes() {
		if strings.Contains(route.Path, "/management") {
			got = append(got, route.Method+" "+route.Path)
		}
	}
	want := []string{
		"GET /v8/management/oauth/callback", "POST /v8/management/oauth/callback",
		"GET /v8/management/config", "GET /v8/management/config/*path",
		"PUT /v8/management/config/access/api-keys", "PUT /v8/management/config/api-keys/:family",
		"POST /v8/management/routing/cooldown/reset",
		"GET /v8/management/routing/model-definitions/:channel",
		"POST /v8/management/routing/model-definitions/refresh", "POST /v8/management/models/refresh",
		"GET /v8/management/credentials", "POST /v8/management/credentials", "DELETE /v8/management/credentials",
		"GET /v8/management/credentials/models", "GET /v8/management/credentials/download",
		"PATCH /v8/management/credentials/status", "PATCH /v8/management/credentials/fields",
		"POST /v8/management/credentials/refresh", "POST /v8/management/oauth/import",
		"GET /v8/management/oauth/auth-url", "POST /v8/management/oauth/auth-url",
		"POST /v8/management/oauth/siwc/bind", "GET /v8/management/oauth/siwc/status",
		"GET /v8/management/credentials/siwc/status", "GET /v8/management/oauth/status",
		"DELETE /v8/management/oauth/session",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("management routes = %q\nwant %q", got, want)
	}
}

func TestManagementAccessControl(t *testing.T) {
	for _, tc := range []struct {
		name, document, remote, key string
		want                        int
	}{
		{"key from loopback", "management: {secret-key: test-password}\n", "127.0.0.1:1234", "test-password", http.StatusOK},
		{"missing key", "management: {secret-key: test-password}\n", "127.0.0.1:1234", "", http.StatusUnauthorized},
		{"wrong key", "management: {secret-key: test-password}\n", "127.0.0.1:1234", "other", http.StatusUnauthorized},
		{"remote address", "management: {secret-key: test-password}\n", "10.77.0.2:1234", "test-password", http.StatusForbidden},
		{"remote address allowed", "management: {secret-key: test-password, allow-remote: true}\n", "10.77.0.2:1234", "test-password", http.StatusOK},
		{"no management key", "server: {port: 8317}\n", "127.0.0.1:1234", "test-password", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newV8ManagementServer(t, tc.document)
			if got := serveManagement(s, http.MethodGet, "/v8/management/config", tc.remote, tc.key); got.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", got.Code, tc.want, got.Body)
			}
		})
	}
}

func TestManagementConfigIsReadOnly(t *testing.T) {
	s := newV8ManagementServer(t, "routing:\n  retry: {request-retry: 3}\nmanagement: {secret-key: test-password}\n")
	got := serveManagement(s, http.MethodGet, "/v8/management/config/routing/retry/request-retry", "127.0.0.1:1234", "test-password")
	if got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != "3" {
		t.Fatalf("nested value: status %d, body %s; want 3", got.Code, got.Body)
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := serveManagement(s, method, "/v8/management/config", "127.0.0.1:1234", "test-password"); got.Code != http.StatusNotFound {
			t.Errorf("%s /config: status %d, want 404", method, got.Code)
		}
	}
}

func TestManagementStartsAndCancelsOAuthLogins(t *testing.T) {
	s := newV8ManagementServer(t, "management: {secret-key: test-password}\n")
	request := func(method, target string, status int) string {
		t.Helper()
		got := serveManagement(s, method, target, "127.0.0.1:1234", "test-password")
		if got.Code != status {
			t.Fatalf("%s %s: status %d, want %d; body %s", method, target, got.Code, status, got.Body)
		}
		return strings.TrimSpace(got.Body.String())
	}
	for _, tc := range []struct{ target, body string }{
		{"/v8/management/oauth/auth-url", `{"error":"provider is required"}`},
		{"/v8/management/oauth/auth-url?provider=%20", `{"error":"provider is required"}`},
	} {
		if got := request(http.MethodGet, tc.target, http.StatusBadRequest); got != tc.body {
			t.Fatalf("%s: body %s, want %s", tc.target, got, tc.body)
		}
	}
	request(http.MethodGet, "/v8/management/oauth/auth-url?provider=unknown", http.StatusNotFound)
	for _, tc := range []struct{ target, provider string }{
		{"/v8/management/oauth/auth-url?provider=codex", "codex"},
		{"/v8/management/oauth/auth-url?provider=%20CLAUDE%20", "anthropic"},
	} {
		var login struct{ URL, State string }
		if err := json.Unmarshal([]byte(request(http.MethodGet, tc.target, http.StatusOK)), &login); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { management.CancelOAuthSession(login.State) })
		if login.URL == "" || !management.IsOAuthSessionPending(login.State, tc.provider) {
			t.Fatalf("%s: no pending login", tc.target)
		}
		if got := request(http.MethodGet, "/v8/management/oauth/status?state="+login.State, http.StatusOK); got != `{"status":"wait"}` {
			t.Fatalf("pending login status = %s", got)
		}
		request(http.MethodDelete, "/v8/management/oauth/session?state="+login.State, http.StatusOK)
		if management.IsOAuthSessionPending(login.State, tc.provider) {
			t.Fatal("the login was not cancelled")
		}
	}
}
