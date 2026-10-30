//go:build component

package cliproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/AsheTheWings/cliapiproxy/internal/api"
	"github.com/AsheTheWings/cliapiproxy/internal/keystate"
)

// A client key change through the management API takes effect in the running
// service: the replaced key loses access, the new one gains it, and an empty
// list refuses everyone, all without a restart.
func TestManagementClientKeyChangesReachTheRunningService(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("management-key"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf("config-version: 8\nmanagement: {secret-key: %q, allow-remote: true}\noauth: {auth-dir: %q}\n", hash, t.TempDir())
	store, err := keystate.New(filepath.Join(t.TempDir(), "keys.yaml"), []byte(document), []byte("access: {api-keys: [client-key-a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Config()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBuilder().WithConfig(cfg).WithServerOptions(api.WithKeyStore(store)).Build()
	if err != nil {
		t.Fatal(err)
	}
	service.server = api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	handler := service.server.Handler()

	models := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	putClientKeys := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/v8/management/config/access/api-keys", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer management-key")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: status %d, body %s", body, rec.Code, rec.Body)
		}
	}

	if got := models("client-key-a"); got != http.StatusOK {
		t.Fatalf("initial key: status %d, want 200", got)
	}
	putClientKeys(`["client-key-b"]`)
	if got := models("client-key-a"); got != http.StatusUnauthorized {
		t.Errorf("replaced key: status %d, want 401", got)
	}
	if got := models("client-key-b"); got != http.StatusOK {
		t.Errorf("new key: status %d, want 200", got)
	}
	putClientKeys(`[]`)
	if got := models("client-key-b"); got != http.StatusUnauthorized {
		t.Errorf("after the last key's removal: status %d, want 401", got)
	}
}
