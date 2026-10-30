// Package kiro implements the Kiro service client for API keys: the model
// catalog of the account behind a key, and the errors of the Kiro hosts.
package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultRuntimeURL serves inference.
	DefaultRuntimeURL = "https://runtime.us-east-1.kiro.dev"
	// DefaultManagementURL serves the model catalog.
	DefaultManagementURL = "https://management.us-east-1.kiro.dev"

	// Origin is the origin the agent names on inference and catalog requests.
	Origin = "AI_EDITOR"
	// TokenType is the TokenType header value that marks the Bearer value as an
	// API key. Without it the service treats the key as a login token and asks
	// for a profile.
	TokenType = "API_KEY"

	// RefreshInterval is how long an account's catalog stays current before
	// the credential reloads it.
	RefreshInterval = time.Hour
	// RefreshLead is how long before that moment a credential reloads.
	RefreshLead = time.Minute

	maxResponseBytes = 8 << 20
)

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

// IsAccessDenied reports whether the service denied the request.
func (e *APIError) IsAccessDenied() bool {
	if e.Status != http.StatusForbidden && e.Status != http.StatusBadRequest {
		return false
	}
	if strings.EqualFold(e.Code, "access_denied") {
		return true
	}
	return strings.Contains(e.Type, "AccessDeniedException")
}

// IsInvalidToken reports whether the service rejected the Bearer value itself,
// as opposed to refusing the account something.
func (e *APIError) IsInvalidToken() bool {
	if !e.IsAccessDenied() {
		return false
	}
	message := strings.ToLower(e.Message)
	return strings.Contains(message, "bearer token") || strings.Contains(message, "invalid token") ||
		strings.Contains(message, "token expired")
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

// Client calls the Kiro management host.
type Client struct {
	httpClient    *http.Client
	managementURL string
}

// NewClientForHost returns a client that sends its requests with httpClient to
// the given management host.
func NewClientForHost(httpClient *http.Client, managementURL string) *Client {
	return &Client{httpClient: httpClient, managementURL: strings.TrimRight(managementURL, "/")}
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

// ListAvailableModels returns the models the account behind apiKey may use.
// The service derives the account and its profile from the key.
func (c *Client) ListAvailableModels(ctx context.Context, apiKey string) (*Catalog, error) {
	header := http.Header{}
	header.Set("Content-Type", "application/x-amz-json-1.0")
	header.Set("X-Amz-Target", "KiroControlPlaneBearerService.ListAvailableModels")
	header.Set("User-Agent", ControlPlaneUserAgent)
	header.Set("X-Amz-User-Agent", ControlPlaneAmzUserAgent)
	header.Set("X-Amzn-Codewhisperer-Optout", "true")
	header.Set("TokenType", TokenType)
	header.Set("Authorization", "Bearer "+apiKey)
	header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	status, data, err := c.postJSON(ctx, c.managementURL+"/", map[string]string{"origin": Origin}, header)
	if err != nil {
		return nil, fmt.Errorf("kiro: list available models: %w", err)
	}
	if status < 200 || status > 299 {
		return nil, ParseAPIError(status, data)
	}
	return ParseCatalog(data)
}
