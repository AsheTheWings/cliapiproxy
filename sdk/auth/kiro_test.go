//go:build unit

package auth

import (
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/auth/kiro"
)

func TestKiroRefreshLeadStaysBeforeTheReloadMoment(t *testing.T) {
	lead := NewKiroAuthenticator().RefreshLead()
	if lead == nil || *lead <= 0 || *lead >= kiro.RefreshInterval {
		t.Errorf("refresh lead = %v", lead)
	}
}
