package cliproxy

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// accountCatalogKey names the metadata entry that holds the model catalog of a
// provider whose credentials load their account's catalog on each reload.
func accountCatalogKey(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case commandcodeauth.Provider:
		return commandcodeauth.CatalogMetadataKey, true
	case kiroauth.Provider:
		return kiroauth.CatalogMetadataKey, true
	default:
		return "", false
	}
}

// catalogFingerprint identifies the catalog a credential's metadata holds.
func catalogFingerprint(metadata map[string]any, key string) [sha256.Size]byte {
	data, _ := json.Marshal(metadata[key])
	return sha256.Sum256(data)
}

// buildAccountCatalogConfigModels publishes the catalog models a group lists,
// in the group's order. A listed model takes every property it does not set
// from its catalog entry. A model the account's catalog lacks is not
// published, because the account decides the models its key may use.
func buildAccountCatalogConfigModels(models []config.MetaModel, catalog []*ModelInfo) []*ModelInfo {
	byID := make(map[string]*ModelInfo, len(catalog))
	for _, info := range catalog {
		if info != nil {
			byID[strings.ToLower(info.ID)] = info
		}
	}
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, configured := range models {
		name := strings.TrimSpace(configured.Name)
		entry, inCatalog := byID[strings.ToLower(name)]
		if !inCatalog {
			continue
		}
		// Catalog infos are built for this call, so a shallow copy shares
		// nothing another registration holds.
		info := *entry
		if alias := strings.TrimSpace(configured.Alias); alias != "" {
			info.ID = alias
			info.MetadataModelID = name
		}
		if _, dup := seen[strings.ToLower(info.ID)]; dup {
			continue
		}
		seen[strings.ToLower(info.ID)] = struct{}{}
		if displayName := strings.TrimSpace(configured.DisplayName); displayName != "" {
			info.DisplayName = displayName
		}
		if configured.MaxContextLength > 0 {
			info.ContextLength = configured.MaxContextLength
			info.MaxContextLength = configured.MaxContextLength
		}
		if configured.Thinking != nil {
			info.Thinking = modelconfig.NormalizeThinkingSupport(configured.Thinking)
			info.ExplicitThinking = true
		}
		out = append(out, &info)
	}
	return out
}

// accountCatalogModels publishes an account-catalog credential's models: the
// catalog its metadata holds, or the models its key's group lists from it.
// Without a catalog, which the first reload loads, it publishes none.
func accountCatalogModels(entries []config.CommandCodeKey, auth *coreauth.Auth, catalog []*ModelInfo, hasCatalog bool) []*ModelInfo {
	if !hasCatalog {
		return nil
	}
	if entry := resolveConfigAccountKey(entries, auth); entry != nil && len(entry.Models) > 0 {
		return buildAccountCatalogConfigModels(entry.Models, catalog)
	}
	return catalog
}

// accountKeys returns the configured keys of an account-catalog provider.
func accountKeys(cfg *config.Config, provider string) []config.CommandCodeKey {
	if cfg == nil {
		return nil
	}
	switch provider {
	case commandcodeauth.Provider:
		return cfg.CommandCodeKey
	case kiroauth.Provider:
		return cfg.KiroKey
	}
	return nil
}

// resolveConfigAccountKey returns the configuration entry a credential was
// synthesized from: the entry at its config index when that entry still holds
// its key, otherwise the first entry with its key and prefix.
func resolveConfigAccountKey(entries []config.CommandCodeKey, auth *coreauth.Auth) *config.CommandCodeKey {
	if auth == nil || auth.Attributes == nil {
		return nil
	}
	key := strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	if key == "" {
		return nil
	}
	if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex])); errIndex == nil && index >= 0 && index < len(entries) && entries[index].APIKey == key {
		return &entries[index]
	}
	for i := range entries {
		if entries[i].APIKey == key && strings.EqualFold(entries[i].Prefix, strings.TrimSpace(auth.Prefix)) {
			return &entries[i]
		}
	}
	return nil
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
