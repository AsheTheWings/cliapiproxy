package auth

import (
	"context"
	"errors"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/auth/commandcode"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
)

var commandCodeRefreshLead = commandcode.RefreshLead

// CommandCodeAuthenticator schedules the reloads of Command Code credentials.
// Their API keys are key state the management API writes, so it has no login.
type CommandCodeAuthenticator struct{}

// NewCommandCodeAuthenticator constructs a Command Code authenticator.
func NewCommandCodeAuthenticator() Authenticator {
	return &CommandCodeAuthenticator{}
}

// Provider returns the provider key for Command Code.
func (CommandCodeAuthenticator) Provider() string {
	return commandcode.Provider
}

// RefreshLead returns how long before its reload moment a Command Code
// credential reloads its plan and catalog.
func (CommandCodeAuthenticator) RefreshLead() *time.Duration {
	return &commandCodeRefreshLead
}

// Login fails: Command Code API keys come from the key state.
func (CommandCodeAuthenticator) Login(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return nil, errors.New("commandcode: API keys are added through the management API; there is no login")
}
