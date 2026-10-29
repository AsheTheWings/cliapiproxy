//go:build component

package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClientForHosts(server.Client(), server.URL, server.URL)
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func readJSON(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err = json.Unmarshal(body, &out); err != nil {
		t.Fatalf("request body %q: %v", body, err)
	}
	return out
}

func TestRefreshSendsTheRefreshTokenAndKeepsIt(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/refreshToken" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != "Kiro-CLI" {
			t.Errorf("User-Agent = %q", got)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("refresh carries an Authorization header")
		}
		if got := readJSON(t, r)["refreshToken"]; got != "refresh-1" {
			t.Errorf("refreshToken = %q", got)
		}
		_, _ = w.Write([]byte(`{"accessToken":"access-1","expiresIn":3600,"profileArn":"arn:profile","refreshToken":"refresh-1"}`))
	}))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }

	tokens, err := client.Refresh(context.Background(), " refresh-1\n")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" || tokens.ProfileARN != "arn:profile" {
		t.Errorf("tokens = %+v", tokens)
	}
	if want := now.Add(time.Hour); !tokens.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want %v", tokens.ExpiresAt, want)
	}
}

func TestRefreshReportsABadToken(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	_, err := client.Refresh(context.Background(), "wrong")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode() != http.StatusUnauthorized || apiErr.Message != "Bad credentials" {
		t.Fatalf("error = %v", err)
	}
	if _, err = client.Refresh(context.Background(), "  "); err == nil {
		t.Error("an empty refresh token was accepted")
	}
}

func TestDeviceFlowPollsUntilTheUserApproves(t *testing.T) {
	var polls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device/authorization":
			body := readJSON(t, r)
			if body["clientId"] != "Kiro-CLI" || body["loginProvider"] != "Google" {
				t.Errorf("authorization body = %v", body)
			}
			_, _ = w.Write([]byte(`{"deviceCode":"device-1","userCode":"AAAA-BBBB","verificationUri":"https://example.test/device","verificationUriComplete":"https://example.test/device?user_code=AAAA-BBBB","expiresInMilliseconds":300000,"intervalInMilliseconds":5000}`))
		case "/oauth/device/poll":
			if body := readJSON(t, r); body["deviceCode"] != "device-1" || body["clientId"] != "Kiro-CLI" {
				t.Errorf("poll body = %v", body)
			}
			switch polls.Add(1) {
			case 1:
				_, _ = w.Write([]byte(`{"status":"pending"}`))
			case 2:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			default:
				_, _ = w.Write([]byte(`{"accessToken":"access-1","refreshToken":"refresh-1","identityProvider":"Google","profileArn":"arn:profile","status":"authorized"}`))
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))

	device, err := client.StartDeviceFlow(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if device.UserCode != "AAAA-BBBB" || device.VerificationURIComplete == "" {
		t.Errorf("device = %+v", device)
	}
	tokens, err := client.WaitForAuthorization(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "refresh-1" || tokens.IdentityProvider != "Google" || polls.Load() != 3 {
		t.Errorf("tokens = %+v after %d polls", tokens, polls.Load())
	}
}

func TestDeviceFlowStopsWhenTheCodeExpires(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	client.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}
	_, err := client.WaitForAuthorization(context.Background(), &DeviceAuthorization{
		DeviceCode: "device-1", ExpiresInMilliseconds: 20000, IntervalInMilliseconds: 5000,
	})
	if err == nil {
		t.Fatal("polling went on after the code expired")
	}
}

func TestDeviceFlowFailsOnARejectedCode(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid device code"}`))
	}))
	_, err := client.WaitForAuthorization(context.Background(), &DeviceAuthorization{DeviceCode: "x", ExpiresInMilliseconds: 60000})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
}

func TestListAvailableModelsRequestAndAccessDenied(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "KiroControlPlaneBearerService.ListAvailableModels" ||
			r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.Header.Get("User-Agent") != ControlPlaneUserAgent || r.Header.Get("X-Amzn-Codewhisperer-Optout") != "true" {
			t.Errorf("fingerprint headers = %v", r.Header)
		}
		if r.Header.Get("Amz-Sdk-Invocation-Id") == "" {
			t.Error("no invocation id")
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazon.kiro.controlplane#AccessDeniedException","message":"Token expired"}`))
			return
		}
		if body := readJSON(t, r); body["origin"] != "KIRO_CLI" || body["profileArn"] != "arn:profile" {
			t.Errorf("body = %v", body)
		}
		_, _ = w.Write([]byte(catalogResponse))
	}))

	catalog, err := client.ListAvailableModels(context.Background(), "good", "arn:profile")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].ModelID != "claude-haiku-5.5" {
		t.Errorf("catalog = %+v", catalog)
	}

	_, err = client.ListAvailableModels(context.Background(), "stale", "arn:profile")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsInvalidToken() {
		t.Fatalf("error = %v, want an invalid token error", err)
	}
}

const catalogResponse = `{"defaultModel":{"modelId":"auto"},"models":[{"modelId":"claude-haiku-5.5","tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":64000}}]}`

func TestInvalidTokenIsNotAnEntitlementDenial(t *testing.T) {
	invalid := ParseAPIError(http.StatusForbidden, []byte(`{"error":{"message":"The bearer token included in the request is invalid.","type":"access_denied","param":null,"code":"access_denied"}}`))
	if !invalid.IsInvalidToken() {
		t.Errorf("%+v is not an invalid token", invalid)
	}
	other := ParseAPIError(http.StatusForbidden, []byte(`{"error":{"message":"This model is not available on your plan.","type":"access_denied","code":"access_denied"}}`))
	if other.IsInvalidToken() || !other.IsAccessDenied() {
		t.Errorf("%+v should be an access denial only", other)
	}
}
