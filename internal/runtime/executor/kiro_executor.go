package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	authURL       string
	managementURL string
}

// NewKiroExecutor constructs a Kiro executor.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{
		cfg:           cfg,
		inferenceURL:  kiroInferenceURL,
		authURL:       kiroauth.DefaultAuthURL,
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
	token, _ := kiroCreds(auth)
	if token == "" {
		req.Header.Del("Authorization")
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
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

// Refresh exchanges the refresh token for a new access token and reloads the
// model catalog of the account. A failed catalog load keeps the last catalog.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "kiro executor: auth is nil"}
	}
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "kiro executor: the credential has no refresh token"}
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := kiroauth.NewClientForHosts(httpClient, e.authURL, e.managementURL)
	tokens, err := client.Refresh(ctx, refreshToken)
	if err != nil {
		var apiErr *kiroauth.APIError
		if errors.As(err, &apiErr) {
			return nil, statusErr{code: apiErr.Status, msg: apiErr.Error()}
		}
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	now := time.Now().UTC()
	auth.Metadata["type"] = kiroauth.Provider
	auth.Metadata["access_token"] = tokens.AccessToken
	auth.Metadata["refresh_token"] = tokens.RefreshToken
	if !tokens.ExpiresAt.IsZero() {
		auth.Metadata["expired"] = tokens.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if tokens.ProfileARN != "" {
		auth.Metadata["profile_arn"] = tokens.ProfileARN
	}
	auth.Metadata["last_refresh"] = now.Format(time.RFC3339)

	_, profileARN := kiroCreds(auth)
	catalog, errCatalog := client.ListAvailableModels(ctx, tokens.AccessToken, profileARN)
	if errCatalog == nil {
		var stored map[string]any
		if stored, errCatalog = catalog.Metadata(now); errCatalog == nil {
			auth.Metadata[kiroauth.CatalogMetadataKey] = stored
		}
	}
	if errCatalog != nil {
		log.Warnf("kiro executor: keeping the previous model catalog: %v", errCatalog)
	}
	auth.LastRefreshedAt = now
	return auth, nil
}

// kiroCreds returns the access token and profile ARN of a credential.
func kiroCreds(auth *cliproxyauth.Auth) (token, profileARN string) {
	if auth == nil {
		return "", ""
	}
	if value, ok := auth.Metadata["access_token"].(string); ok {
		token = strings.TrimSpace(value)
	}
	if value, ok := auth.Metadata["profile_arn"].(string); ok {
		profileARN = strings.TrimSpace(value)
	}
	return token, profileARN
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

// applyKiroHeaders sets the headers of the Kiro agent's inference requests.
func applyKiroHeaders(req *http.Request, token, profileARN string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-amzn-kiro-origin", kiroauth.InferenceOrigin)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("x-amzn-kiro-profile", profileARN)
	req.Header.Set("User-Agent", kiroauth.InferenceUserAgent)
	req.Header.Set("x-kiro-attempt", "1;max=3")
}

// wrapKiroUpstreamError turns a failed inference response into an error the
// conductor classifies. The service reports a rejected access token as 403, so
// that case is reported as 401, which makes the conductor refresh the token and
// retry.
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
