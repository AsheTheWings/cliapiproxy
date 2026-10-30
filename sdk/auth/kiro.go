package auth

import (
	"context"
	"errors"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/auth/kiro"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
)

var kiroRefreshLead = kiro.RefreshLead

// KiroAuthenticator schedules the catalog reloads of Kiro credentials. Their
// API keys are inputs, so it has no login.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a Kiro authenticator.
func NewKiroAuthenticator() Authenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key for Kiro.
func (KiroAuthenticator) Provider() string {
	return kiro.Provider
}

// RefreshLead returns how long before its reload moment a Kiro credential
// reloads its catalog.
func (KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

// Login fails: Kiro API keys come from the api-keys.kiro-keys input.
func (KiroAuthenticator) Login(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return nil, errors.New("kiro: API keys come from the CLIPROXY_KIRO_KEYS input; there is no login")
}
