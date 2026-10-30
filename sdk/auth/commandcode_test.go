//go:build unit

package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
)

func TestCommandCodeRefreshLeadStaysBeforeTheReloadMoment(t *testing.T) {
	lead := NewCommandCodeAuthenticator().RefreshLead()
	if lead == nil || *lead <= 0 || *lead >= commandcode.RefreshInterval {
		t.Errorf("refresh lead = %v", lead)
	}
}
