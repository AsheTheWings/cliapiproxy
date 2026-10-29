//go:build component

package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func fakeKiroService(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refreshToken":
			if !strings.Contains(readBody(r), `"refreshToken":"refresh-1"`) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"access-1","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:111:profile/TEST","refreshToken":"refresh-1"}`))
		default:
			_, _ = w.Write([]byte(`{"defaultModel":{"modelId":"auto"},"models":[{"modelId":"deepseek-3.2","tokenLimits":{"maxInputTokens":164000,"maxOutputTokens":64000}}]}`))
		}
	}))
	t.Cleanup(server.Close)
	old := newKiroClient
	newKiroClient = func(*config.Config) *kiro.Client {
		return kiro.NewClientForHosts(server.Client(), server.URL, server.URL)
	}
	t.Cleanup(func() { newKiroClient = old })
}

func readBody(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	return string(body)
}

func writeTokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKiroImportBuildsTheCredentialFromARefreshToken(t *testing.T) {
	fakeKiroService(t)
	path := writeTokenFile(t, "refresh-1\n", 0o600)
	auth, err := NewKiroAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
		Metadata: map[string]string{KiroRefreshTokenFileOption: path},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if auth.Provider != "kiro" || !strings.HasPrefix(auth.FileName, "kiro-") || !strings.HasSuffix(auth.FileName, ".json") || auth.ID != auth.FileName {
		t.Errorf("auth = %+v", auth)
	}
	meta := auth.Metadata
	if meta["type"] != "kiro" || meta["access_token"] != "access-1" || meta["refresh_token"] != "refresh-1" ||
		meta["profile_arn"] != "arn:aws:codewhisperer:us-east-1:111:profile/TEST" {
		t.Errorf("metadata = %v", meta)
	}
	expired, err := time.Parse(time.RFC3339, meta["expired"].(string))
	if err != nil || time.Until(expired) < 59*time.Minute {
		t.Errorf("expired = %v (%v)", meta["expired"], err)
	}
	catalog, ok := kiro.CatalogFromMetadata(meta)
	if !ok || len(catalog.Models) != 1 || catalog.Models[0].ModelID != "deepseek-3.2" {
		t.Errorf("catalog = %+v", catalog)
	}

	again, err := NewKiroAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
		Metadata: map[string]string{KiroRefreshTokenFileOption: path},
	})
	if err != nil || again.FileName != auth.FileName {
		t.Errorf("importing the same account gave file %q (%v), want %q", again.FileName, err, auth.FileName)
	}
}

func TestKiroImportRefusesUnsafeOrBadTokenFiles(t *testing.T) {
	fakeKiroService(t)
	cases := map[string]string{
		"readable by others": writeTokenFile(t, "refresh-1", 0o644),
		"empty":              writeTokenFile(t, "  \n", 0o600),
		"rejected by Kiro":   writeTokenFile(t, "other", 0o600),
		"missing":            filepath.Join(t.TempDir(), "absent"),
	}
	for name, path := range cases {
		_, err := NewKiroAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
			Metadata: map[string]string{KiroRefreshTokenFileOption: path},
		})
		if err == nil {
			t.Errorf("%s: the import succeeded", name)
		}
	}
}

func TestKiroRefreshLeadCoversTheEarlyRejection(t *testing.T) {
	lead := NewKiroAuthenticator().RefreshLead()
	if lead == nil || *lead < 5*time.Minute {
		t.Errorf("refresh lead = %v, want at least 5 minutes", lead)
	}
}
