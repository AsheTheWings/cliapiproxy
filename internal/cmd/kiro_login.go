package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroLogin signs in to Kiro with the device flow and saves the credential.
func DoKiroLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	loginKiro(cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
}

// DoKiroImport saves a Kiro credential for the refresh token that the file at
// path holds.
func DoKiroImport(cfg *config.Config, path string) {
	loginKiro(cfg, &sdkAuth.LoginOptions{
		Metadata: map[string]string{sdkAuth.KiroRefreshTokenFileOption: path},
	})
}

func loginKiro(cfg *config.Config, authOpts *sdkAuth.LoginOptions) {
	record, savedPath, err := newAuthManager().Login(context.Background(), "kiro", cfg, authOpts)
	if err != nil {
		log.Errorf("Kiro authentication failed: %v", err)
		return
	}
	fmt.Println("\nKiro authentication successful!")
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
}
