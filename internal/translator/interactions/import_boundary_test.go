package interactions_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInteractionsTranslatorsDoNotImportGeminiTranslators(t *testing.T) {
	repoRoot := moduleRoot(t)
	scanDirs := []string{
		"internal/translator/openai/interactions",
		"internal/translator/claude/interactions",
		"internal/translator/codex/interactions",
		"internal/translator/antigravity/interactions",
	}
	forbidden := regexp.MustCompile(`"github\.com/router-for-me/CLIProxyAPI(/v[0-9]+)?/internal/translator/[^"]*/gemini[^"]*"`)
	var violations []string
	for _, scanDir := range scanDirs {
		root := filepath.Join(repoRoot, scanDir)
		errWalk := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				return errRead
			}
			if forbidden.Match(data) {
				rel, errRel := filepath.Rel(repoRoot, path)
				if errRel != nil {
					rel = path
				}
				violations = append(violations, rel)
			}
			return nil
		})
		if errWalk != nil {
			t.Fatalf("scan %s: %v", scanDir, errWalk)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("non-Gemini Interactions translators import Gemini translators: %s", strings.Join(violations, ", "))
	}
}

// moduleRoot returns the directory holding go.mod above the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, errWd := os.Getwd()
	if errWd != nil {
		t.Fatal(errWd)
	}
	for {
		if _, errStat := os.Stat(filepath.Join(dir, "go.mod")); errStat == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}
