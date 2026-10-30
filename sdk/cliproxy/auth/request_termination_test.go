//go:build unit

package auth

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
)

func TestRequestTerminatedErrorSkipsCreditsFallback(t *testing.T) {
	errTerminated := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests}
	if !isRequestTerminatedError(errTerminated) {
		t.Fatal("isRequestTerminatedError() = false")
	}
	if shouldAttemptAntigravityCreditsFallback(&Manager{}, errTerminated, []string{"antigravity"}) {
		t.Fatal("terminated request must not use Antigravity credits fallback")
	}
}
