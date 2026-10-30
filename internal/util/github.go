package util

import "github.com/AsheTheWings/cliapiproxy/internal/githubauth"

// ResolveGitHubToken returns the configured GitHub API token, or empty.
func ResolveGitHubToken() string {
	return githubauth.ResolveToken()
}
