package util

import "github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"

// ResolveGitHubToken returns the configured GitHub API token, or empty.
func ResolveGitHubToken() string {
	return githubauth.ResolveToken()
}
