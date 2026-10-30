//go:build component

package synthesizer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAuthFilesOfAPIKeyOnlyProvidersAreIgnored(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"kiro-abc.json": `{"type":"kiro","access_token":"a","refresh_token":"r","profile_arn":"arn:p",
		  "kiro_catalog":{"fetched_at":"2026-10-29T12:00:00Z","models":[{"modelId":"deepseek-3.2"}]}}`,
		"commandcode-abc.json": `{"type":"commandcode","api_key":"k"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{}, AuthDir: dir, Now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 0 {
		t.Errorf("auths = %+v; files of providers whose accounts are API keys must not load", auths)
	}
}
