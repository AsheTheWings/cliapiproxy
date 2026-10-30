//go:build component

package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListAvailableModelsSendsTheKeyAsTheAgentDoes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "KiroControlPlaneBearerService.ListAvailableModels" ||
			r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.Header.Get("User-Agent") != ControlPlaneUserAgent || r.Header.Get("X-Amz-User-Agent") != ControlPlaneAmzUserAgent ||
			r.Header.Get("X-Amzn-Codewhisperer-Optout") != "true" {
			t.Errorf("fingerprint headers = %v", r.Header)
		}
		if r.Header.Get("Amz-Sdk-Invocation-Id") == "" {
			t.Error("no invocation id")
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil || len(body) != 1 || body["origin"] != "AI_EDITOR" {
			t.Errorf("body = %s; the catalog request names no profile", data)
		}
		if r.Header.Get("Authorization") != "Bearer good" || r.Header.Get("TokenType") != "API_KEY" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazon.kiro.controlplane#AccessDeniedException","message":"Invalid token"}`))
			return
		}
		_, _ = w.Write([]byte(catalogResponse))
	}))
	t.Cleanup(server.Close)
	client := NewClientForHost(server.Client(), server.URL)

	catalog, err := client.ListAvailableModels(context.Background(), "good")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].ModelID != "claude-haiku-5.5" {
		t.Errorf("catalog = %+v", catalog)
	}

	_, err = client.ListAvailableModels(context.Background(), "revoked")
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
