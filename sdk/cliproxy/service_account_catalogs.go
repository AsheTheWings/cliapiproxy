package cliproxy

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// accountCatalogKey names the metadata entry that holds the model catalog of a
// provider whose credentials load their account's catalog on each reload.
func accountCatalogKey(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case commandcodeauth.Provider:
		return commandcodeauth.CatalogMetadataKey, true
	default:
		return "", false
	}
}

// catalogFingerprint identifies the catalog a credential's metadata holds.
func catalogFingerprint(metadata map[string]any, key string) [sha256.Size]byte {
	data, _ := json.Marshal(metadata[key])
	return sha256.Sum256(data)
}

// followAccountCatalog republishes the models of an account-catalog credential
// each time a refresh changes the catalog in its metadata; no file changes, so
// the watcher does not republish it. One follower runs per credential until
// the credential is removed or the service stops.
func (s *Service) followAccountCatalog(registered *coreauth.Auth) {
	if s == nil || s.coreManager == nil || registered == nil || registered.ID == "" {
		return
	}
	key, ok := accountCatalogKey(registered.Provider)
	if !ok || !coreauth.IsAccountCatalogAuth(registered) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime == nil || lifetime.Err() != nil {
		return
	}
	id := registered.ID
	s.catalogFollowMu.Lock()
	if _, running := s.catalogFollowers[id]; running {
		s.catalogFollowMu.Unlock()
		return
	}
	if s.catalogFollowers == nil {
		s.catalogFollowers = make(map[string]struct{})
	}
	s.catalogFollowers[id] = struct{}{}
	s.catalogFollowMu.Unlock()

	// Subscribe before the first comparison, so a reload between the
	// registration and the subscription is still seen.
	changes, unsubscribe := s.coreManager.WatchAuthChanges(id)
	published := catalogFingerprint(registered.Metadata, key)
	go func() {
		defer func() {
			unsubscribe()
			s.catalogFollowMu.Lock()
			delete(s.catalogFollowers, id)
			s.catalogFollowMu.Unlock()
		}()
		for {
			latest, exists := s.coreManager.GetByID(id)
			if !exists || latest == nil {
				return
			}
			if current := catalogFingerprint(latest.Metadata, key); current != published {
				published = current
				s.completeModelRegistrationForAuth(lifetime, latest)
			}
			select {
			case <-lifetime.Done():
				return
			case _, open := <-changes:
				if !open {
					return
				}
			}
		}
	}()
}
