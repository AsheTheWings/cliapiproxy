package management

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/clienterror"
	"github.com/AsheTheWings/cliapiproxy/internal/providers"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// PutProviderGroups replaces one API-key family's upstream groups. The body
// is the family's whole list as GET /config shows it. Groups the server's
// inputs declare must come back unchanged; the others become the provider
// state. A key sent back masked keeps the stored key it masks.
func (h *Handler) PutProviderGroups(c *gin.Context) {
	family := c.Param("family")
	if !upstream.KnownFamily(family) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	body, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid_body"})
		return
	}
	incoming, errDecode := decodeProviderGroups(body)
	if errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_groups", "message": errDecode.Error()})
		return
	}

	h.mu.Lock()
	store := h.providerStore
	if !store.Enabled() {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "provider_state_off"})
		return
	}
	inputs, stored := store.Groups(family)
	state, errSplit := stateGroups(incoming, inputs, stored)
	if errSplit != nil {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "groups_conflict", "message": errSplit.Error()})
		return
	}
	cfg, errReplace := store.Replace(family, state)
	if errReplace != nil {
		h.mu.Unlock()
		if errors.Is(errReplace, providers.ErrInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_groups", "message": errReplace.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed"})
		return
	}
	h.cfg = cfg
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// decodeProviderGroups reads a JSON list of groups, ignoring the auth indexes
// GET /config adds to each key.
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

// stateGroups splits an incoming family list: groups named like the inputs'
// groups must equal them as GET /config shows them, and the rest are the
// state, with masked keys replaced by the stored keys they mask.
func stateGroups(incoming, inputs, stored []upstream.Group) ([]upstream.Group, error) {
	inputByName := make(map[string]upstream.Group, len(inputs))
	for _, g := range inputs {
		inputByName[g.Name] = g
	}
	storedByName := make(map[string]upstream.Group, len(stored))
	for _, g := range stored {
		storedByName[g.Name] = g
	}
	var state []upstream.Group
	for _, g := range incoming {
		if input, isInput := inputByName[g.Name]; isInput {
			if !reflect.DeepEqual(maskedGroup(input), maskedGroup(g)) {
				return nil, errors.New("group " + g.Name + " comes from the server's inputs and cannot change here")
			}
			continue
		}
		group, err := unmaskKeys(g, storedByName[g.Name])
		if err != nil {
			return nil, err
		}
		state = append(state, group)
	}
	return state, nil
}

// maskedGroup is a group as GET /config shows it.
func maskedGroup(g upstream.Group) upstream.Group {
	out := upstream.Group{Spec: g.Spec, Keys: make([]upstream.Key, len(g.Keys))}
	for i, k := range g.Keys {
		out.Keys[i] = upstream.Key{APIKey: maskSecret(k.APIKey)}
		if strings.HasPrefix(k.APIKey, "****") {
			out.Keys[i] = k
		}
	}
	return out
}

// unmaskKeys replaces each masked key of g with the stored key of the same
// group that masks to it, taking stored keys in order. A masked key that no
// stored key masks to is a conflict: the key has to be entered again.
func unmaskKeys(g, stored upstream.Group) (upstream.Group, error) {
	out := upstream.Group{Spec: g.Spec, Keys: make([]upstream.Key, len(g.Keys))}
	used := make([]bool, len(stored.Keys))
	for i, k := range g.Keys {
		out.Keys[i] = k
		if !strings.HasPrefix(k.APIKey, "****") {
			continue
		}
		found := false
		for j, s := range stored.Keys {
			if !used[j] && maskSecret(s.APIKey) == k.APIKey {
				out.Keys[i], used[j], found = s, true, true
				break
			}
		}
		if !found {
			return upstream.Group{}, fmt.Errorf("group %s: key %d is masked and matches no stored key; enter it again", g.Name, i+1)
		}
	}
	return out, nil
}

// providerGroupsNode renders each family's groups, the inputs' first, with
// their keys, as the api-keys subtree of the v8 layout.
func providerGroupsNode(store *providers.Store) (*yaml.Node, error) {
	view := map[string][]upstream.Group{}
	for _, family := range upstream.Families() {
		inputs, state := store.Groups(family)
		if groups := append(inputs, state...); len(groups) > 0 {
			view[family] = groups
		}
	}
	var node yaml.Node
	if err := node.Encode(view); err != nil {
		return nil, err
	}
	return &node, nil
}
