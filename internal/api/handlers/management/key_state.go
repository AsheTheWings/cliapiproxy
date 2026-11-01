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
// keeps the stored key it masks, in the group of the same name or, for a
// renamed group, in the group it replaces at the same position. A key's
// label is trimmed, and a label given empty is rejected.
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
	incoming, errDecode := decodeProviderGroups(family, body)
	if errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_groups", "message": errDecode.Error()})
		return
	}
	h.replaceKeyState(c, func(store *keystate.Store) (*config.Config, error) {
		current := store.Groups(family)
		groups := make([]upstream.Group, len(incoming))
		for i, g := range incoming {
			keys := make([]string, len(g.Keys))
			for j, k := range g.Keys {
				keys[j] = k.APIKey
			}
			var storedKeys []string
			for _, k := range sourceGroup(current, incoming, i).Keys {
				storedKeys = append(storedKeys, k.APIKey)
			}
			unmasked, err := unmaskKeys(keys, storedKeys)
			if err != nil {
				return nil, fmt.Errorf("%w: group %s: %v", errMasked, g.Name, err)
			}
			groups[i] = upstream.Group{Spec: g.Spec, Keys: make([]upstream.Key, len(unmasked))}
			for j, k := range unmasked {
				groups[i].Keys[j] = upstream.Key{APIKey: k, Label: g.Keys[j].Label}
			}
		}
		return store.ReplaceGroups(family, groups)
	})
}

// PutClientKeys replaces the client API keys and their labels. The body is
// the whole list of objects of api-key and an optional label, as
// GET /config/access/api-keys shows them; a key sent back masked keeps the
// stored key it masks, and the fingerprint GET adds is ignored. A label is
// trimmed, and a label given empty is rejected. An empty list refuses every
// proxy request.
func (h *Handler) PutClientKeys(c *gin.Context) {
	body, ok := readBody(c)
	if !ok {
		return
	}
	incoming, err := decodeClientKeys(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_keys", "message": err.Error()})
		return
	}
	h.replaceKeyState(c, func(store *keystate.Store) (*config.Config, error) {
		values := make([]string, len(incoming))
		for i, k := range incoming {
			values[i] = k.APIKey
		}
		var stored []string
		for _, k := range store.ClientKeys() {
			stored = append(stored, k.APIKey)
		}
		unmasked, err := unmaskKeys(values, stored)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errMasked, err)
		}
		keys := make([]upstream.Key, len(incoming))
		for i, k := range incoming {
			keys[i] = upstream.Key{APIKey: unmasked[i], Label: k.Label}
		}
		return store.ReplaceClientKeys(keys)
	})
}

// sourceGroup returns the stored group whose keys the incoming group at
// position i unmasks against: the stored group of the same name, or, when the
// group was renamed, the stored group at the same position whose name the
// incoming list no longer uses.
func sourceGroup(stored, incoming []upstream.Group, i int) upstream.Group {
	for _, g := range stored {
		if g.Name == incoming[i].Name {
			return g
		}
	}
	if i >= len(stored) {
		return upstream.Group{}
	}
	for _, g := range incoming {
		if g.Name == stored[i].Name {
			return upstream.Group{}
		}
	}
	return stored[i]
}

// clientKeyItem is a client key with its label, as PUT takes it.
type clientKeyItem struct {
	APIKey      string  `json:"api-key"`
	Label       *string `json:"label"`
	Fingerprint string  `json:"fingerprint"`
}

// decodeClientKeys reads a JSON list of objects of api-key and label,
// returning the keys with their trimmed labels in order. Errors name keys by
// position, never by value.
func decodeClientKeys(body []byte) ([]upstream.Key, error) {
	errShape := errors.New("want a JSON list of objects of api-key and label")
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil || items == nil {
		return nil, errShape
	}
	keys := make([]upstream.Key, len(items))
	for i, raw := range items {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var item clientKeyItem
		if err := decoder.Decode(&item); err != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			return nil, errShape
		}
		keys[i].APIKey = item.APIKey
		if item.Label != nil {
			label, err := upstream.NormalizeLabel(*item.Label)
			if err != nil {
				return nil, fmt.Errorf("access.api-keys: key %d: %v", i+1, err)
			}
			keys[i].Label = label
		}
	}
	return keys, nil
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
// and fingerprints GET /config adds, and trims each key's label.
func decodeProviderGroups(family string, body []byte) ([]upstream.Group, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil || len(node.Content) == 0 || node.Content[0].Kind != yaml.SequenceNode {
		return nil, errors.New("want a JSON list of groups")
	}
	for g, group := range node.Content[0].Content {
		deleteConfigV8Path(group, []string{"auth_index"})
		deleteConfigV8Path(group, []string{"auth-index"})
		if keys := configV8Node(group, []string{"keys"}); keys != nil {
			name := fmt.Sprintf("%d", g+1)
			if n := configV8Node(group, []string{"name"}); n != nil && n.Kind == yaml.ScalarNode {
				name = n.Value
			}
			for k, key := range keys.Content {
				deleteConfigV8Path(key, []string{"auth_index"})
				deleteConfigV8Path(key, []string{"auth-index"})
				deleteConfigV8Path(key, []string{"fingerprint"})
				label := configV8Node(key, []string{"label"})
				if label == nil {
					continue
				}
				if label.Kind == yaml.ScalarNode && label.Tag == "!!null" {
					deleteConfigV8Path(key, []string{"label"})
					continue
				}
				if label.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("api-keys.%s: group %s key %d: label must be a string", family, name, k+1)
				}
				trimmed, err := upstream.NormalizeLabel(label.Value)
				if err != nil {
					return nil, fmt.Errorf("api-keys.%s: group %s key %d: %v", family, name, k+1, err)
				}
				label.Value, label.Tag, label.Style = trimmed, "!!str", 0
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
		return nil, errors.New("want groups of name, base-url, prefix, models, and keys of api-key and label")
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
// api-keys subtree of the v8 layout, each key with its fingerprint.
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
		for g, group := range list.Content {
			keys := configV8Node(group, []string{"keys"})
			if keys == nil {
				continue
			}
			for k, key := range keys.Content {
				key.Content = append(key.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "fingerprint"},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: upstream.Fingerprint(groups[g].Keys[k].APIKey)})
			}
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: family}, &list)
	}
	return root, nil
}

// keyStateClientKeysNode renders the client keys as objects of api-key,
// label, and fingerprint.
func keyStateClientKeysNode(store *keystate.Store) (*yaml.Node, error) {
	type item struct {
		APIKey      string `yaml:"api-key"`
		Label       string `yaml:"label,omitempty"`
		Fingerprint string `yaml:"fingerprint"`
	}
	keys := store.ClientKeys()
	items := make([]item, len(keys))
	for i, k := range keys {
		items[i] = item{APIKey: k.APIKey, Label: k.Label, Fingerprint: upstream.Fingerprint(k.APIKey)}
	}
	var list yaml.Node
	if err := list.Encode(items); err != nil {
		return nil, err
	}
	return &list, nil
}
