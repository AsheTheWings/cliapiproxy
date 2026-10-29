package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KiroRefreshTokenFileOption names the LoginOptions metadata entry that makes
// a login import the refresh token a file holds instead of starting a device
// login.
const KiroRefreshTokenFileOption = "refresh_token_file"

var kiroRefreshLead = kiro.RefreshLead

// newKiroClient builds the service client of a login; tests replace it.
var newKiroClient = func(cfg *config.Config) *kiro.Client { return kiro.NewClient(cfg, "") }

// KiroAuthenticator logs in to Kiro with the device flow or from a refresh token.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a Kiro authenticator.
func NewKiroAuthenticator() Authenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key for Kiro.
func (KiroAuthenticator) Provider() string {
	return kiro.Provider
}

// RefreshLead returns how long before expiry a Kiro credential refreshes.
func (KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

// Login signs in with the device flow, or imports the refresh token of the file
// named by the KiroRefreshTokenFileOption option.
func (a KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	client := newKiroClient(cfg)

	if path := strings.TrimSpace(opts.Metadata[KiroRefreshTokenFileOption]); path != "" {
		refreshToken, err := readKiroRefreshToken(path)
		if err != nil {
			return nil, err
		}
		return kiroCredential(ctx, client, refreshToken, "")
	}

	fmt.Println("Starting Kiro authentication...")
	device, err := client.StartDeviceFlow(ctx, kiro.DefaultLoginProvider)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to start device flow: %w", err)
	}
	verificationURL := strings.TrimSpace(device.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(device.VerificationURI)
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if device.UserCode != "" {
		fmt.Printf("User code: %s\n\n", device.UserCode)
	}
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	if device.ExpiresInMilliseconds > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", device.ExpiresInMilliseconds/1000)
	}
	tokens, err := client.WaitForAuthorization(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("kiro: %w", err)
	}
	return kiroCredential(ctx, client, tokens.RefreshToken, tokens.IdentityProvider)
}

// readKiroRefreshToken reads a refresh token from a file only its owner can
// access.
func readKiroRefreshToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("kiro: read refresh token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("kiro: the refresh token file is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("kiro: the refresh token file is accessible by other users; restrict it to its owner")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("kiro: read refresh token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("kiro: the refresh token file is empty")
	}
	return token, nil
}

// kiroCredential refreshes the token once, so the credential starts with an
// access token whose expiry the service stated, and records the account's
// model catalog.
func kiroCredential(ctx context.Context, client *kiro.Client, refreshToken, identityProvider string) (*coreauth.Auth, error) {
	tokens, err := client.Refresh(ctx, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("kiro: %w", err)
	}
	if strings.TrimSpace(tokens.ProfileARN) == "" {
		return nil, errors.New("kiro: the service returned no profile for this login")
	}
	catalog, err := client.ListAvailableModels(ctx, tokens.AccessToken, tokens.ProfileARN)
	if err != nil {
		return nil, fmt.Errorf("kiro: %w", err)
	}
	now := time.Now().UTC()
	catalogMetadata, err := catalog.Metadata(now)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{
		"type":                  kiro.Provider,
		"access_token":          tokens.AccessToken,
		"refresh_token":         tokens.RefreshToken,
		"profile_arn":           tokens.ProfileARN,
		"last_refresh":          now.Format(time.RFC3339),
		kiro.CatalogMetadataKey: catalogMetadata,
	}
	if !tokens.ExpiresAt.IsZero() {
		metadata["expired"] = tokens.ExpiresAt.UTC().Format(time.RFC3339)
	}
	label := "Kiro"
	if identityProvider = strings.TrimSpace(identityProvider); identityProvider != "" {
		metadata["identity_provider"] = identityProvider
		label = fmt.Sprintf("Kiro (%s)", identityProvider)
	}
	sum := sha256.Sum256([]byte(tokens.ProfileARN))
	fileName := fmt.Sprintf("kiro-%s.json", hex.EncodeToString(sum[:6]))
	return &coreauth.Auth{
		ID:       fileName,
		Provider: kiro.Provider,
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}
