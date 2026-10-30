package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/clienterror"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/keystate"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// PutProviderGroups replaces one API-key family's upstream groups. The body
// is the family's whole list as GET /config shows it; a key sent back masked
// keeps the stored key it masks.
func (h *Handler) PutProviderGroups(c *gin.Context) {
	family := c.Param("family")
	if !upstream.KnownFamily(family) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	incoming, errDecode := decodeProviderGroups(body)
	if errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_groups", "message": errDecode.Error()})
		return
	}
	h.replaceKeyState(c, func(store *keystate.Store) (*config.Config, error) {
		stored := map[string]upstream.Group{}
		for _, g := range store.Groups(family) {
			stored[g.Name] = g
		}
		groups := make([]upstream.Group, len(incoming))
		for i, g := range incoming {
			keys := make([]string, len(g.Keys))
			for j, k := range g.Keys {
				keys[j] = k.APIKey
			}
			var storedKeys []string
			for _, k := range stored[g.Name].Keys {
				storedKeys = append(storedKeys, k.APIKey)
			}
			unmasked, err := unmaskKeys(keys, storedKeys)
			if err != nil {
				return nil, fmt.Errorf("%w: group %s: %v", errMasked, g.Name, err)
			}
			groups[i] = upstream.Group{Spec: g.Spec, Keys: make([]upstream.Key, len(unmasked))}
			for j, k := range unmasked {
				groups[i].Keys[j] = upstream.Key{APIKey: k}
			}
		}
		return store.ReplaceGroups(family, groups)
	})
}

// PutClientKeys replaces the client API keys. The body is the whole list as
// GET /config/access/api-keys shows it; a key sent back masked keeps the
// stored key it masks. An empty list refuses every proxy request.
func (h *Handler) PutClientKeys(c *gin.Context) {
	body, ok := readBody(c)
	if !ok {
		return
	}
	var incoming []string
	if err := json.Unmarshal(body, &incoming); err != nil || incoming == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_keys", "message": "want a JSON list of keys"})
		return
	}
	h.replaceKeyState(c, func(store *keystate.Store) (*config.Config, error) {
		keys, err := unmaskKeys(incoming, store.ClientKeys())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errMasked, err)
		}
		return store.ReplaceClientKeys(keys)
	})
}

// errMasked marks a masked key that matches no stored key.
var errMasked = errors.New("masked key")

func readBody(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "invalid_body"})
		return nil, false
	}
	return body, true
}

// replaceKeyState applies one change to the key state and hands the
// configuration built from it to the running service.
func (h *Handler) replaceKeyState(c *gin.Context, change func(*keystate.Store) (*config.Config, error)) {
	h.mu.Lock()
	store := h.keyStore
	if store == nil {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "key_state_off"})
		return
	}
	cfg, err := change(store)
	if err != nil {
		h.mu.Unlock()
		switch {
		case errors.Is(err, errMasked):
			c.JSON(http.StatusConflict, gin.H{"error": "keys_conflict", "message": err.Error()})
		case errors.Is(err, keystate.ErrInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_keys", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed"})
		}
		return
	}
	h.cfg = cfg
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// decodeProviderGroups reads a JSON list of groups, ignoring the auth indexes
// GET /config adds.
func decodeProviderGroups(body []byte) ([]upstream.Group, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil || len(node.Content) == 0 || node.Content[0].Kind != yaml.SequenceNode {
		return nil, errors.New("want a JSON list of groups")
	}
	for _, group := range node.Content[0].Content {
		deleteConfigV8Path(group, []string{"auth_index"})
		deleteConfigV8Path(group, []string{"auth-index"})
		if keys := configV8Node(group, []string{"keys"}); keys != nil {
			for _, key := range keys.Content {
				deleteConfigV8Path(key, []string{"auth_index"})
				deleteConfigV8Path(key, []string{"auth-index"})
			}
		}
	}
	data, err := yaml.Marshal(node.Content[0])
	if err != nil {
		return nil, errors.New("want a JSON list of groups")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var groups []upstream.Group
	if err = decoder.Decode(&groups); err != nil {
		return nil, errors.New("want groups of name, base-url, prefix, models, and keys of api-key")
	}
	return groups, nil
}

// unmaskKeys replaces each masked key with the stored key that masks to it,
// taking stored keys in order. A masked key that no stored key masks to has
// to be entered again.
func unmaskKeys(keys, stored []string) ([]string, error) {
	out := make([]string, len(keys))
	used := make([]bool, len(stored))
	for i, k := range keys {
		out[i] = k
		if !strings.HasPrefix(k, "****") {
			continue
		}
		found := false
		for j, s := range stored {
			if !used[j] && maskSecret(s) == k {
				out[i], used[j], found = s, true, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("key %d is masked and matches no stored key; enter it again", i+1)
		}
	}
	return out, nil
}

// keyStateGroupsNode renders each family's groups with their keys as the
// api-keys subtree of the v8 layout.
func keyStateGroupsNode(store *keystate.Store) (*yaml.Node, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, family := range upstream.Families() {
		groups := store.Groups(family)
		if len(groups) == 0 {
			continue
		}
		var list yaml.Node
		if err := list.Encode(groups); err != nil {
			return nil, err
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: family}, &list)
	}
	return root, nil
}
