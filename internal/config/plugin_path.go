package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

const defaultPluginsDir = "plugins"

// ResolvePluginsDir normalizes the plugin directory for consistent use throughout the app.
// It defaults empty values to plugins; a path needing home expansion is an error.
func ResolvePluginsDir(pluginsDir string) (string, error) {
	pluginsDir = strings.TrimSpace(pluginsDir)
	if pluginsDir == "" {
		pluginsDir = defaultPluginsDir
	}
	if strings.HasPrefix(pluginsDir, "~") {
		return "", fmt.Errorf("resolve plugins directory: %q needs home expansion, which is not supported", pluginsDir)
	}
	return filepath.Clean(pluginsDir), nil
}

// ResolvePluginsDir resolves and stores the effective plugin directory.
func (cfg *Config) ResolvePluginsDir() error {
	if cfg == nil {
		return nil
	}
	pluginsDir, errResolvePluginsDir := ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolvePluginsDir != nil {
		return errResolvePluginsDir
	}
	cfg.Plugins.Dir = pluginsDir
	return nil
}
