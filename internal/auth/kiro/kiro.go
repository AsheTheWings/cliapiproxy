// Package kiro implements the Kiro service client: the device login, the
// token refresh, and the model catalog of the account behind a credential.
package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"golang.org/x/sync/singleflight"
)

const (
	// ClientID is the public client identifier of the Kiro CLI.
	ClientID = "Kiro-CLI"
	// DefaultLoginProvider is the identity provider the device login asks for.
	DefaultLoginProvider = "Google"

	// DefaultAuthURL serves the device login and the token refresh.
	DefaultAuthURL = "https://prod.us-east-1.auth.desktop.kiro.dev"
	// DefaultRuntimeURL serves inference.
	DefaultRuntimeURL = "https://runtime.us-east-1.kiro.dev"
	// DefaultManagementURL serves the model catalog.
	DefaultManagementURL = "https://management.us-east-1.kiro.dev"

	// InferenceOrigin is the origin the agent names on inference requests.
	InferenceOrigin = "AI_EDITOR"
	// CatalogOrigin is the origin the launcher names on catalog requests.
	CatalogOrigin = "KIRO_CLI"

	// RefreshLead is how long before expiry a credential refreshes. The service
	// can reject an access token minutes before its stated expiry.
	RefreshLead = 10 * time.Minute

	maxResponseBytes = 8 << 20
)

var refreshGroup singleflight.Group

// APIError is an error response of a Kiro host.
type APIError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("kiro: status %d: %s", e.Status, detail)
}

// StatusCode returns the HTTP status of the response.
func (e *APIError) StatusCode() int { return e.Status }

// IsAccessDenied reports whether the service rejected the access token.
func (e *APIError) IsAccessDenied() bool {
	if e.Status != http.StatusForbidden && e.Status != http.StatusBadRequest {
		return false
	}
	if strings.EqualFold(e.Code, "access_denied") {
		return true
	}
	return strings.Contains(e.Type, "AccessDeniedException")
}

// IsInvalidToken reports whether the service rejected the access token itself,
// as opposed to refusing the account something. Refreshing the token cures it.
func (e *APIError) IsInvalidToken() bool {
	if !e.IsAccessDenied() {
		return false
	}
	message := strings.ToLower(e.Message)
	return strings.Contains(message, "bearer token") || strings.Contains(message, "token expired")
}

// ParseAPIError builds the error of a non-2xx response body. It reads both
// error shapes the hosts use: {"error":{...}} and {"__type","message"}.
func ParseAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{Status: status}
	var shape struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &shape) != nil {
		apiErr.Message = strings.TrimSpace(string(body))
		return apiErr
	}
	apiErr.Type = shape.Type
	apiErr.Message = shape.Message
	if shape.Error != nil {
		apiErr.Message = shape.Error.Message
		apiErr.Type = shape.Error.Type
		if code, ok := shape.Error.Code.(string); ok {
			apiErr.Code = code
		}
	}
	return apiErr
}

// Tokens is the result of a login or a refresh.
type Tokens struct {
	AccessToken      string
	RefreshToken     string
	ProfileARN       string
	IdentityProvider string
	ExpiresAt        time.Time
}

// DeviceAuthorization is the answer to the start of a device login.
type DeviceAuthorization struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresInMilliseconds   int64  `json:"expiresInMilliseconds"`
	IntervalInMilliseconds  int64  `json:"intervalInMilliseconds"`
}

// Client calls the Kiro auth and management hosts.
type Client struct {
	httpClient    *http.Client
	authURL       string
	managementURL string
	now           func() time.Time
	sleep         func(context.Context, time.Duration) error
}

// NewClient returns a client for the production hosts that follows the
// configured proxy; proxyURL overrides it when non-empty.
func NewClient(cfg *config.Config, proxyURL string) *Client {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var sdkCfg config.SDKConfig
	effective := strings.TrimSpace(proxyURL)
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effective == "" {
			effective = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effective
	return NewClientForHosts(util.SetProxy(&sdkCfg, httpClient), DefaultAuthURL, DefaultManagementURL)
}

// NewClientForHosts returns a client that sends its requests with httpClient to
// the given hosts.
func NewClientForHosts(httpClient *http.Client, authURL, managementURL string) *Client {
	return &Client{
		httpClient:    httpClient,
		authURL:       strings.TrimRight(authURL, "/"),
		managementURL: strings.TrimRight(managementURL, "/"),
		now:           time.Now,
		sleep:         sleepContext,
	}
}

// SetHTTPClient replaces the HTTP client.
func (c *Client) SetHTTPClient(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) postJSON(ctx context.Context, endpoint string, body any, header http.Header) (int, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func authHeader() http.Header {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", "Kiro-CLI")
	return header
}

// StartDeviceFlow asks for a device code the user approves in a browser.
func (c *Client) StartDeviceFlow(ctx context.Context, loginProvider string) (*DeviceAuthorization, error) {
	if strings.TrimSpace(loginProvider) == "" {
		loginProvider = DefaultLoginProvider
	}
	status, data, err := c.postJSON(ctx, c.authURL+"/oauth/device/authorization",
		map[string]string{"clientId": ClientID, "loginProvider": loginProvider}, authHeader())
	if err != nil {
		return nil, fmt.Errorf("kiro: start device flow: %w", err)
	}
	if status < 200 || status > 299 {
		return nil, ParseAPIError(status, data)
	}
	var out DeviceAuthorization
	if err = json.Unmarshal(data, &out); err != nil || out.DeviceCode == "" {
		return nil, errors.New("kiro: device authorization response has no device code")
	}
	return &out, nil
}

// WaitForAuthorization polls until the user approves the device code, the code
// expires, or ctx ends. The service answers a poll before approval with a
// status other than "authorized".
func (c *Client) WaitForAuthorization(ctx context.Context, device *DeviceAuthorization) (*Tokens, error) {
	interval := time.Duration(device.IntervalInMilliseconds) * time.Millisecond
	if interval <= 0 {
		interval = 5 * time.Second
	}
	lifetime := time.Duration(device.ExpiresInMilliseconds) * time.Millisecond
	if lifetime <= 0 {
		lifetime = 5 * time.Minute
	}
	deadline := c.now().Add(lifetime)
	transientFailures := 0
	for {
		if err := c.sleep(ctx, interval); err != nil {
			return nil, err
		}
		if c.now().After(deadline) {
			return nil, errors.New("kiro: device code expired before it was approved")
		}
		status, data, err := c.postJSON(ctx, c.authURL+"/oauth/device/poll",
			map[string]string{"clientId": ClientID, "deviceCode": device.DeviceCode}, authHeader())
		if err != nil || status >= 500 {
			transientFailures++
			if transientFailures > 5 {
				if err == nil {
					err = ParseAPIError(status, data)
				}
				return nil, fmt.Errorf("kiro: poll device authorization: %w", err)
			}
			continue
		}
		transientFailures = 0
		var reply struct {
			Status           string `json:"status"`
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			IdentityProvider string `json:"identityProvider"`
			ProfileARN       string `json:"profileArn"`
		}
		_ = json.Unmarshal(data, &reply)
		if status >= 200 && status <= 299 && reply.AccessToken != "" && reply.RefreshToken != "" {
			return &Tokens{
				AccessToken:      reply.AccessToken,
				RefreshToken:     reply.RefreshToken,
				ProfileARN:       reply.ProfileARN,
				IdentityProvider: reply.IdentityProvider,
			}, nil
		}
		switch strings.ToLower(reply.Status) {
		case "expired", "denied", "rejected", "failed", "error":
			return nil, fmt.Errorf("kiro: device authorization %s", strings.ToLower(reply.Status))
		}
		if status >= 400 && !pendingBody(data) {
			return nil, ParseAPIError(status, data)
		}
	}
}

// pendingBody reports whether an error body only says the user has not
// approved yet.
func pendingBody(data []byte) bool {
	text := strings.ToLower(string(data))
	return strings.Contains(text, "pending") || strings.Contains(text, "slow_down")
}

// Refresh exchanges a refresh token for an access token. The refresh token does
// not rotate.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("kiro: refresh token is empty")
	}
	result, err, _ := refreshGroup.Do(refreshToken, func() (any, error) {
		status, data, errPost := c.postJSON(ctx, c.authURL+"/refreshToken",
			map[string]string{"refreshToken": refreshToken}, authHeader())
		if errPost != nil {
			return nil, fmt.Errorf("kiro: refresh token: %w", errPost)
		}
		if status < 200 || status > 299 {
			return nil, ParseAPIError(status, data)
		}
		var reply struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ProfileARN   string `json:"profileArn"`
			ExpiresIn    int64  `json:"expiresIn"`
		}
		if errParse := json.Unmarshal(data, &reply); errParse != nil || reply.AccessToken == "" {
			return nil, errors.New("kiro: refresh response has no access token")
		}
		tokens := &Tokens{
			AccessToken:  reply.AccessToken,
			RefreshToken: reply.RefreshToken,
			ProfileARN:   reply.ProfileARN,
		}
		if tokens.RefreshToken == "" {
			tokens.RefreshToken = refreshToken
		}
		if reply.ExpiresIn > 0 {
			tokens.ExpiresAt = c.now().Add(time.Duration(reply.ExpiresIn) * time.Second)
		}
		return tokens, nil
	})
	if err != nil {
		return nil, err
	}
	tokens := *result.(*Tokens)
	return &tokens, nil
}

// ListAvailableModels returns the models the account behind accessToken may use.
func (c *Client) ListAvailableModels(ctx context.Context, accessToken, profileARN string) (*Catalog, error) {
	header := http.Header{}
	header.Set("Content-Type", "application/x-amz-json-1.0")
	header.Set("X-Amz-Target", "KiroControlPlaneBearerService.ListAvailableModels")
	header.Set("User-Agent", ControlPlaneUserAgent)
	header.Set("X-Amz-User-Agent", ControlPlaneAmzUserAgent)
	header.Set("X-Amzn-Codewhisperer-Optout", "true")
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	header.Set("Accept", "*/*")
	status, data, err := c.postJSON(ctx, c.managementURL+"/",
		map[string]string{"origin": CatalogOrigin, "profileArn": profileARN}, header)
	if err != nil {
		return nil, fmt.Errorf("kiro: list available models: %w", err)
	}
	if status < 200 || status > 299 {
		return nil, ParseAPIError(status, data)
	}
	return ParseCatalog(data)
}
