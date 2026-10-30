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
	"github.com/AsheTheWings/cliapiproxy/internal/keystate"
	"github.com/gin-gonic/gin"
)

const keyStateInputsDocument = `config-version: 8
management: {secret-key: test-password}
oauth: {auth-dir: %q}
`

// newKeyStateServer serves the management routes with a key store whose
// state file lives in a temporary directory, and records each configuration
// the management API hands to the service.
func newKeyStateServer(t *testing.T) (*Server, string, chan *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "keys.yaml")
	store, err := keystate.New(path, []byte(fmt.Sprintf(keyStateInputsDocument, t.TempDir())), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Config()
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, "", nil)
	h.SetKeyStore(store)
	applied := make(chan *config.Config, 4)
	h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { applied <- next })
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	return s, path, applied
}

func putKeys(s *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/v8/management/config/"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	return response
}

func TestManagementKeepsKeysAsState(t *testing.T) {
	s, path, applied := newKeyStateServer(t)
	if got := putKeys(s, "api-keys/kiro", `[{"name":"kiro","prefix":"k","keys":[{"api-key":"state-key-0123456789"}]}]`); got.Code != http.StatusOK {
		t.Fatalf("PUT kiro: status %d, body %s", got.Code, got.Body)
	}
	if got := putKeys(s, "access/api-keys", `[{"api-key":"client-key-0123456789","name":"Alice's laptop"}]`); got.Code != http.StatusOK {
		t.Fatalf("PUT client keys: status %d, body %s", got.Code, got.Body)
	}
	var cfg *config.Config
	for len(applied) > 0 {
		cfg = <-applied
	}
	if cfg == nil || len(cfg.KiroKey) != 1 || cfg.KiroKey[0].APIKey != "state-key-0123456789" || len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "client-key-0123456789" {
		t.Fatalf("applied configuration = %+v", cfg)
	}

	view := serveManagement(s, http.MethodGet, "/v8/management/config", "127.0.0.1:1234", "test-password")
	if view.Code != http.StatusOK || strings.Contains(view.Body.String(), "0123456789") {
		t.Fatalf("GET config: status %d, body %s; want masked keys", view.Code, view.Body)
	}
	var doc struct {
		Access struct {
			APIKeys     []string `json:"api-keys"`
			APIKeyNames []string `json:"api-key-names"`
		} `json:"access"`
		APIKeys map[string][]map[string]any `json:"api-keys"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.APIKeys["kiro"]) != 1 || doc.APIKeys["kiro"][0]["name"] != "kiro" || len(doc.Access.APIKeys) != 1 || len(doc.Access.APIKeyNames) != 1 || doc.Access.APIKeyNames[0] != "Alice's laptop" {
		t.Fatalf("view = %s", view.Body)
	}

	// The view sent back unchanged keeps the stored keys it masks, and a key
	// replaced in place keeps its position and takes its new name.
	kiro, _ := json.Marshal(doc.APIKeys["kiro"])
	clients, _ := json.Marshal([]map[string]string{
		{"api-key": "client-key-replaced", "name": "Alice's desktop"},
		{"api-key": doc.Access.APIKeys[0]},
	})
	for route, body := range map[string]string{"api-keys/kiro": string(kiro), "access/api-keys": string(clients)} {
		if got := putKeys(s, route, body); got.Code != http.StatusOK {
			t.Fatalf("PUT masked %s: status %d, body %s", route, got.Code, got.Body)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state-key-0123456789", "client-key-replaced", "Alice's desktop", "client-key-0123456789"} {
		if !strings.Contains(string(saved), key) {
			t.Fatalf("state file lacks %s:\n%s", key, saved)
		}
	}
}

func TestManagementKeepsMaskedKeysOfARenamedGroup(t *testing.T) {
	s, path, _ := newKeyStateServer(t)
	if got := putKeys(s, "api-keys/kiro", `[{"name":"old-name","keys":[{"api-key":"stored-key-01234567890"}]},{"name":"other","keys":[{"api-key":"other-key-0123456789"}]}]`); got.Code != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", got.Code, got.Body)
	}
	renamed := `[{"name":"new-name","keys":[{"api-key":"****7890"}]},{"name":"other","keys":[{"api-key":"****6789"}]}]`
	if got := putKeys(s, "api-keys/kiro", renamed); got.Code != http.StatusOK {
		t.Fatalf("PUT renamed: status %d, body %s", got.Code, got.Body)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "new-name") || !strings.Contains(string(saved), "stored-key-01234567890") || strings.Contains(string(saved), "old-name") {
		t.Fatalf("state file after the rename:\n%s", saved)
	}
	// A group moved into another's position takes no keys from it.
	swapped := `[{"name":"other","keys":[{"api-key":"****6789"}]},{"name":"third","keys":[{"api-key":"****7890"}]}]`
	if got := putKeys(s, "api-keys/kiro", swapped); got.Code != http.StatusConflict {
		t.Fatalf("PUT swapped: status %d, want 409; body %s", got.Code, got.Body)
	}
}

func TestManagementRefusesChangesItCannotKeep(t *testing.T) {
	s, path, _ := newKeyStateServer(t)
	for _, tc := range []struct {
		name, route, body string
		want              int
	}{
		{"masked key without a stored key", "api-keys/kiro", `[{"name":"kiro","keys":[{"api-key":"****6789"}]}]`, http.StatusConflict},
		{"masked client key without a stored key", "access/api-keys", `["****6789"]`, http.StatusConflict},
		{"group without a key", "api-keys/kiro", `[{"name":"kiro","keys":[]}]`, http.StatusBadRequest},
		{"unknown field", "api-keys/kiro", `[{"name":"kiro","region":"eu","keys":[{"api-key":"k"}]}]`, http.StatusBadRequest},
		{"repeated client key", "access/api-keys", `["k","k"]`, http.StatusBadRequest},
		{"blank client key", "access/api-keys", `[" "]`, http.StatusBadRequest},
		{"padded group key", "api-keys/kiro", `[{"name":"kiro","keys":[{"api-key":" k"}]}]`, http.StatusBadRequest},
		{"client keys not a list", "access/api-keys", `{"k":1}`, http.StatusBadRequest},
		{"client key with an unknown field", "access/api-keys", `[{"api-key":"k","note":"x"}]`, http.StatusBadRequest},
		{"client key name too long", "access/api-keys", `[{"api-key":"k","name":"` + strings.Repeat("n", 65) + `"}]`, http.StatusBadRequest},
		{"unknown family", "api-keys/gemini", `[]`, http.StatusNotFound},
	} {
		if got := putKeys(s, tc.route, tc.body); got.Code != tc.want {
			t.Errorf("%s: status %d, want %d; body %s", tc.name, got.Code, tc.want, got.Body)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused change wrote the state file: %v", err)
	}
}
