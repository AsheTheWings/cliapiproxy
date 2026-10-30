package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	kiroauth "github.com/AsheTheWings/cliapiproxy/internal/auth/kiro"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var _ cliproxyauth.ProviderExecutor = (*KiroExecutor)(nil)

// kiroInferenceURL is the Kiro inference route.
const kiroInferenceURL = kiroauth.DefaultRuntimeURL + "/v1/responses"

// KiroExecutor runs requests on the Kiro inference route with the credential of
// a Kiro account.
type KiroExecutor struct {
	cfg *config.Config
	// The hosts the executor calls; tests replace them.
	inferenceURL  string
	managementURL string
}

// NewKiroExecutor constructs a Kiro executor.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{
		cfg:           cfg,
		inferenceURL:  kiroInferenceURL,
		managementURL: kiroauth.DefaultManagementURL,
	}
}

// Identifier returns the provider key "kiro".
func (e *KiroExecutor) Identifier() string { return kiroauth.Provider }

// SupportsApplyPatch reports that the executor bridges the apply_patch tool.
func (e *KiroExecutor) SupportsApplyPatch() bool { return e != nil }

// PrepareRequest sets the credential of auth on an outgoing request.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := kiroAPIKey(auth)
	if token == "" {
		req.Header.Del("Authorization")
		req.Header.Del("TokenType")
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("TokenType", kiroauth.TokenType)
	return nil
}

// HttpRequest sets the credential on req and sends it.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
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

// Refresh reloads the model catalog of the key's account. An API key has no
// expiry, so the reload also shows that the service still accepts the key; a
// rejected key fails the reload with 401. Any other failed catalog load keeps
// the previous catalog; without one, the refresh fails, so it is retried after
// the refresh backoff.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "kiro executor: auth is nil"}
	}
	apiKey := kiroAPIKey(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "kiro executor: the credential has no API key"}
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := kiroauth.NewClientForHost(httpClient, e.managementURL)
	catalog, errCatalog := client.ListAvailableModels(ctx, apiKey)
	var apiErr *kiroauth.APIError
	if errors.As(errCatalog, &apiErr) && (apiErr.IsInvalidToken() || apiErr.Status == http.StatusUnauthorized) {
		return nil, statusErr{code: http.StatusUnauthorized, msg: apiErr.Error()}
	}
	now := time.Now().UTC()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if errCatalog == nil {
		var stored map[string]any
		if stored, errCatalog = catalog.Metadata(now); errCatalog == nil {
			auth.Metadata[kiroauth.CatalogMetadataKey] = stored
		}
	}
	if errCatalog != nil {
		if _, hasCatalog := kiroauth.CatalogFromMetadata(auth.Metadata); !hasCatalog {
			return nil, fmt.Errorf("kiro executor: load the first model catalog: %w", errCatalog)
		}
		log.Warnf("kiro executor: keeping the previous model catalog: %v", errCatalog)
	}
	auth.Metadata["type"] = kiroauth.Provider
	auth.Metadata["last_refresh"] = now.Format(time.RFC3339)
	auth.Metadata["expired"] = now.Add(kiroauth.RefreshInterval).Format(time.RFC3339)
	auth.LastRefreshedAt = now
	return auth, nil
}

// kiroAPIKey returns the API key of a credential.
func kiroAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey])
}

// kiroCatalogModel returns the catalog entry of an upstream model id, when the
// credential holds a catalog that lists it.
func kiroCatalogModel(auth *cliproxyauth.Auth, modelID string) (*kiroauth.CatalogModel, bool) {
	if auth == nil {
		return nil, false
	}
	catalog, ok := kiroauth.CatalogFromMetadata(auth.Metadata)
	if !ok {
		return nil, false
	}
	model, ok := catalog.Model(modelID)
	if !ok {
		return nil, false
	}
	return &model, true
}

// applyKiroHeaders sets the headers the Kiro agent sends on inference requests
// when it runs on an API key. The key names the account and its profile, so
// no profile is sent.
func applyKiroHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("TokenType", kiroauth.TokenType)
	req.Header.Set("x-amzn-kiro-origin", kiroauth.Origin)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("User-Agent", kiroauth.InferenceUserAgent)
	req.Header.Set("x-kiro-attempt", "1;max=3")
}

// wrapKiroUpstreamError turns a failed inference response into an error the
// conductor classifies. The service reports a rejected key as 403; it is
// reported as 401, the status of a rejected credential.
func wrapKiroUpstreamError(status int, header http.Header, body []byte) error {
	apiErr := kiroauth.ParseAPIError(status, body)
	if apiErr.IsInvalidToken() {
		status = http.StatusUnauthorized
	}
	se := statusErr{code: status, msg: string(body)}
	if status == http.StatusTooManyRequests {
		if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
			retryAfter := time.Duration(seconds) * time.Second
			se.retryAfter = &retryAfter
		}
	}
	return se
}
