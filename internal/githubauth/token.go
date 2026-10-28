// Package githubauth provides shared GitHub API credentials.
package githubauth

import (
	"net/url"
	"strings"
	"sync/atomic"
)

var configuredToken atomic.Pointer[string]

// SetToken replaces the global configured token, including clearing it on reload.
func SetToken(token string) {
	token = strings.TrimSpace(token)
	configuredToken.Store(&token)
}

// ReplaceTokenForTest sets the token and returns a function restoring the
// previous state, for tests.
func ReplaceTokenForTest(token string) (restore func()) {
	previous := configuredToken.Load()
	SetToken(token)
	return func() { configuredToken.Store(previous) }
}

// ResolveToken returns the configured token, or empty when none is set.
func ResolveToken() string {
	if token := configuredToken.Load(); token != nil {
		return *token
	}
	return ""
}

// TokenForURL never exposes the shared credential outside the HTTPS GitHub API.
func TokenForURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.github.com") || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ""
	}
	return ResolveToken()
}
