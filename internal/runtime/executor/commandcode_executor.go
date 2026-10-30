package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	commandcodeauth "github.com/AsheTheWings/cliapiproxy/internal/auth/commandcode"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var _ cliproxyauth.ProviderExecutor = (*CommandCodeExecutor)(nil)

// commandCodeGenerateURL is the gateway's agent route.
const commandCodeGenerateURL = commandcodeauth.DefaultAPIURL + "/alpha/generate"

// commandCodeProjectSlug names the working directory the request reports: the
// CLI's slug (separators replaced by hyphens, the leading one removed) of the
// neutral directory /work the request body names.
const commandCodeProjectSlug = "work"

// CommandCodeExecutor runs requests on the Command Code gateway with the API
// key of an account.
type CommandCodeExecutor struct {
	cfg *config.Config
	// The hosts the executor calls; tests replace them.
	generateURL string
	apiURL      string
}

// NewCommandCodeExecutor constructs a Command Code executor.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{
		cfg:         cfg,
		generateURL: commandCodeGenerateURL,
		apiURL:      commandcodeauth.DefaultAPIURL,
	}
}

// Identifier returns the provider key "commandcode".
func (e *CommandCodeExecutor) Identifier() string { return commandcodeauth.Provider }

// SupportsApplyPatch reports that the executor bridges the apply_patch tool.
func (e *CommandCodeExecutor) SupportsApplyPatch() bool { return e != nil }

// PrepareRequest sets the credential of auth on an outgoing request.
func (e *CommandCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		req.Header.Del("Authorization")
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return nil
}

// HttpRequest sets the credential on req and sends it.
func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("commandcode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh reloads the plan, the credits, and the model catalog of the account.
// An API key has no expiry, so the refresh also checks that the gateway still
// accepts the key. A failed catalog load keeps the previous catalog; without
// one, the refresh fails, so it is retried after the refresh backoff.
func (e *CommandCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "commandcode executor: auth is nil"}
	}
	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "commandcode executor: the credential has no API key"}
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := commandcodeauth.NewClientForHost(httpClient, e.apiURL)

	account, err := client.Account(ctx, apiKey)
	if err != nil {
		var apiErr *commandcodeauth.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			return nil, statusErr{code: apiErr.Status, msg: apiErr.Error()}
		}
		return nil, err
	}
	now := time.Now().UTC()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	catalog, errCatalog := client.Catalog(ctx, apiKey)
	if errCatalog == nil {
		catalog.Account = *account
		var stored map[string]any
		if stored, errCatalog = catalog.Metadata(now); errCatalog == nil {
			auth.Metadata[commandcodeauth.CatalogMetadataKey] = stored
		}
	}
	if errCatalog != nil {
		if _, hasCatalog := commandcodeauth.CatalogFromMetadata(auth.Metadata); !hasCatalog {
			return nil, fmt.Errorf("commandcode executor: load the first model catalog: %w", errCatalog)
		}
		log.Warnf("commandcode executor: keeping the previous model catalog: %v", errCatalog)
	}
	auth.Metadata["type"] = commandcodeauth.Provider
	auth.Metadata["last_refresh"] = now.Format(time.RFC3339)
	auth.Metadata["expired"] = now.Add(commandcodeauth.RefreshInterval).Format(time.RFC3339)
	auth.LastRefreshedAt = now
	return auth, nil
}

// commandCodeAPIKey returns the API key of a credential.
func commandCodeAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey])
}

// commandCodeCatalogModel returns the catalog entry of a gateway model id, when
// the credential holds a catalog that lists it.
func commandCodeCatalogModel(auth *cliproxyauth.Auth, modelID string) (*commandcodeauth.CatalogModel, bool) {
	if auth == nil {
		return nil, false
	}
	catalog, ok := commandcodeauth.CatalogFromMetadata(auth.Metadata)
	if !ok {
		return nil, false
	}
	model, ok := catalog.Model(modelID)
	if !ok {
		return nil, false
	}
	return &model, true
}

// applyCommandCodeHeaders sets the headers of the Command Code CLI's requests
// to the agent route. x-taste-learning is false: the gateway learns a user's
// style from the conversations that carry true.
func applyCommandCodeHeaders(req *http.Request, apiKey, sessionID string) {
	commandcodeauth.ApplyHeaders(req, apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("x-session-id", sessionID)
	req.Header.Set("x-project-slug", commandCodeProjectSlug)
	req.Header.Set("x-taste-learning", "false")
}

// wrapCommandCodeUpstreamError turns a failed response into an error the
// conductor classifies. The gateway answers with the status of the failure, so
// the status passes through.
func wrapCommandCodeUpstreamError(status int, header http.Header, body []byte) error {
	se := statusErr{code: status, msg: string(body)}
	if status == http.StatusTooManyRequests {
		if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
			retryAfter := time.Duration(seconds) * time.Second
			se.retryAfter = &retryAfter
		}
	}
	return se
}
