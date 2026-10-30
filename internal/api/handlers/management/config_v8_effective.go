package management

import (
	"net/http"
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// ConfigV8Effective serves the configuration the server runs with, in the v8
// layout. The key state's client keys and upstream groups change through
// their own routes; the server's inputs own every other value. Each protected value is redacted: client and upstream API keys
// keep only a short suffix, and the management key, the proxy URL, and relay
// credentials are left out.
func (h *Handler) ConfigV8Effective(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := config.RenderV8(h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render_failed"})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render_failed"})
		return
	}
	root := doc.Content[0]
	path := strings.Trim(c.Param("path"), "/")
	var parts []string
	if path != "" {
		parts = strings.Split(path, "/")
	}
	// The key state keeps each group's name and keys together, which the
	// rendered layout loses for families of per-key entries.
	if store := h.keyStore; store != nil {
		groups, errGroups := keyStateGroupsNode(store)
		if errGroups != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "render_failed"})
			return
		}
		deleteConfigV8Path(root, []string{"api-keys"})
		if len(groups.Content) > 0 {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "api-keys"}, groups)
		}
	}
	// Auth indexes derive from the keys, so they are added before redaction.
	h.injectV8APIKeyAuthIndexesLocked(root, data)
	redactV8Secrets(root)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	value := configV8Node(root, parts)
	if value == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var result any
	if err = value.Decode(&result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decode_failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

// redactV8Secrets removes or masks every protected value in a v8 document.
func redactV8Secrets(root *yaml.Node) {
	deleteConfigV8Path(root, []string{"management", "secret-key"})
	deleteConfigV8Path(root, []string{"requests", "proxy-url"})
	deleteConfigV8Path(root, []string{"server", "github-token"})
	if servers := configV8Node(root, v8ICEServersPath); servers != nil && servers.Kind == yaml.SequenceNode {
		for _, server := range servers.Content {
			deleteConfigV8Path(server, []string{"username"})
			deleteConfigV8Path(server, []string{"credential"})
		}
	}
	if keys := configV8Node(root, []string{"access", "api-keys"}); keys != nil {
		maskScalars(keys)
	}
	if families := configV8Node(root, []string{"api-keys"}); families != nil {
		maskFields(families, "api-key")
	}
}

// maskFields masks the scalar value of every mapping field named field
// anywhere under node.
func maskFields(node *yaml.Node, field string) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == field {
				maskScalars(node.Content[i+1])
				continue
			}
			maskFields(node.Content[i+1], field)
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			maskFields(item, field)
		}
	}
}

// maskScalars masks node when it is a scalar, or each scalar item of a list.
func maskScalars(node *yaml.Node) {
	switch node.Kind {
	case yaml.ScalarNode:
		node.Value = maskSecret(node.Value)
		node.Tag = "!!str"
		node.Style = 0
	case yaml.SequenceNode:
		for _, item := range node.Content {
			maskScalars(item)
		}
	}
}

// maskSecret keeps the last four characters of a long secret, so an operator
// can tell keys apart, and hides a short one entirely.
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) < 16 {
		return "****"
	}
	return "****" + value[len(value)-4:]
}
