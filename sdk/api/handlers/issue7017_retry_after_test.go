//go:build unit

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkconfig "github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	coreexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	"github.com/gin-gonic/gin"
)

// Issue 7017: with header passthrough enabled, the raw upstream Retry-After
// carried in Addon must not overwrite the locally trusted cooldown deadline.
func TestIssue7017TrustedRetryAfterWinsOverPassthroughAddon(t *testing.T) {
	gin.SetMode(gin.TestMode)
	next := time.Now().Add(256 * time.Second)
	_, err := (&coreauth.RoundRobinSelector{}).Pick(context.Background(), "codex", "gpt-6.1-sol", coreexecutor.Options{}, []*coreauth.Auth{{
		ID: "synthetic", Provider: "codex", Unavailable: true, NextRetryAfter: next,
		Quota: coreauth.QuotaState{Exceeded: true},
	}})
	if err == nil {
		t.Fatal("expected unavailable credential")
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      err,
		Addon:      http.Header{"Retry-After": {"30"}, "X-Request-Id": {"req-1"}},
	})

	if got := recorder.Header().Get("Retry-After"); got != "256" {
		t.Fatalf("trusted Retry-After must win over passthrough, got %q want 256", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("other passthrough headers must still apply, got %q", got)
	}

	// Without a trusted deadline the raw passthrough value still applies.
	recorderPlain := httptest.NewRecorder()
	cPlain, _ := gin.CreateTestContext(recorderPlain)
	cPlain.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	handler.WriteErrorResponse(cPlain, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon:      http.Header{"Retry-After": {"30"}},
	})
	if got := recorderPlain.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("passthrough Retry-After without trusted deadline = %q, want 30", got)
	}
}
