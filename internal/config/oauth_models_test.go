//go:build unit

package config

import (
	"reflect"
	"testing"
)

func TestNormalizeOAuthModelsFoldsProviderSpellings(t *testing.T) {
	got := NormalizeOAuthModels(map[string][]string{
		"Kimi.AI":  {" Kimi-K3 "},
		"kimi-ai":  {"kimi-k3", "kimi-k2"},
		"kimi.com": {"kimi-k2"},
		"codex":    {" ", ""},
	})
	want := map[string][]string{"kimi-ai": {"kimi-k3", "kimi-k2"}, "kimi": {"kimi-k2"}}
	// Map iteration orders the merged kimi-ai list either way.
	if len(got) != 2 || !reflect.DeepEqual(got["kimi"], want["kimi"]) || len(got["kimi-ai"]) != 2 {
		t.Errorf("NormalizeOAuthModels = %v, want %v", got, want)
	}
}
