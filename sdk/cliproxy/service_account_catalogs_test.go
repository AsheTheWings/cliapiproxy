//go:build component

package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/testsupport/wait"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A configured key is registered at start like every config API key, and its
// refreshes publish the catalog they load.
func TestAConfiguredAccountKeyRegistersAtStartAndPublishesEachCatalog(t *testing.T) {
	cfg := &config.Config{CommandCodeKey: []internalconfig.CommandCodeKey{{APIKey: "k"}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager, antigravityContext: ctx}
	service.registerConfigAPIKeyAuths(ctx, cfg)

	auths := manager.List()
	if len(auths) != 1 || !coreauth.IsAccountCatalogAuth(auths[0]) {
		t.Fatalf("registered auths = %+v", auths)
	}
	id := auths[0].ID
	registry := internalregistry.GetGlobalRegistry()
	t.Cleanup(func() { registry.UnregisterClient(id) })
	if got := registeredCommandCodeModels(id); len(got) != 0 {
		t.Fatalf("a credential without a catalog published %v", got)
	}

	// A refresh stores the account's catalog; the credential stays in memory.
	current, _ := manager.GetByID(id)
	reloaded := current.Clone()
	reloaded.Metadata = commandCodeCatalogMetadata("individual-pro", 0)
	if _, err := manager.Update(ctx, reloaded); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 5*time.Second, func() bool { return len(registeredCommandCodeModels(id)) == 2 },
		"the reloaded catalog was not published: %v", registeredCommandCodeModels(id))

	// Synthesizing the same key again, as a config reload does, keeps the
	// loaded catalog.
	service.registerConfigAPIKeyAuths(ctx, cfg)
	if latest, _ := manager.GetByID(id); latest.Metadata["commandcode_catalog"] == nil {
		t.Errorf("a repeated synthesis dropped the catalog: %v", latest.Metadata)
	}

	// Removing the credential stops its follower.
	service.applyCoreAuthRemoval(ctx, id)
	wait.Until(t, 5*time.Second, func() bool {
		service.catalogFollowMu.Lock()
		defer service.catalogFollowMu.Unlock()
		return len(service.catalogFollowers) == 0
	}, "the follower outlived its credential")
}
