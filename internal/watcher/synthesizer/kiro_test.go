//go:build component

package synthesizer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestFileSynthesizerLoadsAKiroCredentialWithItsCatalog(t *testing.T) {
	dir := t.TempDir()
	file := `{"type":"kiro","access_token":"a","refresh_token":"r","profile_arn":"arn:p","expired":"2026-10-29T13:00:00Z",
	  "kiro_catalog":{"fetched_at":"2026-10-29T12:00:00Z","models":[{"modelId":"deepseek-3.2","tokenLimits":{"maxInputTokens":164000}}]}}`
	if err := os.WriteFile(filepath.Join(dir, "kiro-abc.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{}, AuthDir: dir, Now: time.Now(), IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 1 || auths[0].Provider != "kiro" {
		t.Fatalf("auths = %+v", auths)
	}
	catalog, ok := kiro.CatalogFromMetadata(auths[0].Metadata)
	if !ok || len(catalog.Models) != 1 {
		t.Errorf("the catalog did not reach the credential: %v", auths[0].Metadata)
	}
	if got, _ := auths[0].Metadata["profile_arn"].(string); got != "arn:p" {
		t.Errorf("profile_arn = %q", got)
	}
}
