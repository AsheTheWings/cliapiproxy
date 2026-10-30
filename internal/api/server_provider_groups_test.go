//go:build component

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/api/handlers/management"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/providers"
	"github.com/gin-gonic/gin"
)

const providerInputsDocument = `config-version: 8
management: {secret-key: test-password}
oauth: {auth-dir: %q}
api-keys:
  commandcode:
    - {name: inputs, keys: [{api-key: input-key-0123456789}]}
`

// newProviderStateServer serves the management routes with a provider store
// whose state file lives in a temporary directory, and records each
// configuration the management API hands to the service.
func newProviderStateServer(t *testing.T) (*Server, string, chan *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store, err := providers.New(path, []byte(fmt.Sprintf(providerInputsDocument, t.TempDir())), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Config()
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, "", nil)
	h.SetProviderStore(store)
	applied := make(chan *config.Config, 4)
	h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { applied <- next })
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	return s, path, applied
}

func putGroups(s *Server, family, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/"+family, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	return response
}

func TestManagementKeepsProviderGroupsAsState(t *testing.T) {
	s, path, applied := newProviderStateServer(t)
	if got := putGroups(s, "kiro", `[{"name":"kiro","prefix":"k","keys":[{"api-key":"state-key-0123456789"}]}]`); got.Code != http.StatusOK {
		t.Fatalf("PUT kiro: status %d, body %s", got.Code, got.Body)
	}
	select {
	case cfg := <-applied:
		if len(cfg.KiroKey) != 1 || cfg.KiroKey[0].APIKey != "state-key-0123456789" {
			t.Fatalf("applied kiro keys = %+v", cfg.KiroKey)
		}
	default:
		t.Fatal("the change reached no configuration reload")
	}

	view := serveManagement(s, http.MethodGet, "/v8/management/config/api-keys", "127.0.0.1:1234", "test-password")
	if view.Code != http.StatusOK || strings.Contains(view.Body.String(), "0123456789") {
		t.Fatalf("GET api-keys: status %d, body %s; want masked keys", view.Code, view.Body)
	}
	var groups map[string][]map[string]any
	if err := json.Unmarshal(view.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups["kiro"]) != 1 || groups["kiro"][0]["name"] != "kiro" || groups["kiro"][0]["read-only"] != nil ||
		len(groups["commandcode"]) != 1 || groups["commandcode"][0]["name"] != "inputs" || groups["commandcode"][0]["read-only"] != true {
		t.Fatalf("groups = %v", groups)
	}

	// The view sent back unchanged keeps the stored keys it masks.
	kiro, _ := json.Marshal(groups["kiro"])
	if got := putGroups(s, "kiro", string(kiro)); got.Code != http.StatusOK {
		t.Fatalf("PUT masked kiro: status %d, body %s", got.Code, got.Body)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), "state-key-0123456789") {
		t.Fatalf("state file = %q (%v), want the stored key", saved, err)
	}
	commandcode, _ := json.Marshal(append(groups["commandcode"], map[string]any{"name": "cc", "keys": []map[string]string{{"api-key": "cc-key"}}}))
	if got := putGroups(s, "commandcode", string(commandcode)); got.Code != http.StatusOK {
		t.Fatalf("PUT commandcode with the inputs group unchanged: status %d, body %s", got.Code, got.Body)
	}
}

func TestManagementRefusesChangesItCannotKeep(t *testing.T) {
	s, path, _ := newProviderStateServer(t)
	for _, tc := range []struct {
		name, family, body string
		want               int
	}{
		{"changed inputs group", "commandcode", `[{"name":"inputs","prefix":"p","keys":[{"api-key":"****6789"}]}]`, http.StatusConflict},
		{"masked key without a stored key", "kiro", `[{"name":"kiro","keys":[{"api-key":"****6789"}]}]`, http.StatusConflict},
		{"group without a key", "kiro", `[{"name":"kiro","keys":[]}]`, http.StatusBadRequest},
		{"unknown field", "kiro", `[{"name":"kiro","region":"eu","keys":[{"api-key":"k"}]}]`, http.StatusBadRequest},
		{"unknown family", "gemini", `[]`, http.StatusNotFound},
	} {
		if got := putGroups(s, tc.family, tc.body); got.Code != tc.want {
			t.Errorf("%s: status %d, want %d; body %s", tc.name, got.Code, tc.want, got.Body)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused change wrote the state file: %v", err)
	}
}
