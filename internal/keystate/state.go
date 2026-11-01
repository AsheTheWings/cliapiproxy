package keystate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"gopkg.in/yaml.v3"
)

// State is the keys the server keeps: the client API keys that reach the
// proxy and each API-key family's upstream groups with their keys, every key
// with an optional label. The state file holds it in the configuration's v8
// layout, under access.api-keys and api-keys.
type State struct {
	ClientKeys []upstream.Key              `yaml:"-"`
	Upstream   map[string][]upstream.Group `yaml:"-"`
}

type stateAccess struct {
	APIKeys []upstream.Key `yaml:"api-keys,omitempty"`
}

type stateDocument struct {
	Access  *stateAccess                `yaml:"access,omitempty"`
	APIKeys map[string][]upstream.Group `yaml:"api-keys,omitempty"`
}

type mergedAccess struct {
	APIKeys []string `yaml:"api-keys,omitempty"`
}

// mergedDocument is the part of the configuration document the state adds.
type mergedDocument struct {
	Access  *mergedAccess               `yaml:"access,omitempty"`
	APIKeys map[string][]upstream.Group `yaml:"api-keys,omitempty"`
}

// Parse decodes and validates a state file; empty data is the empty state.
// Errors name groups and positions, never keys.
func Parse(data []byte) (State, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return State{}, nil
	}
	if err := checkLabelTypes(data); err != nil {
		return State{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var doc stateDocument
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return State{}, fmt.Errorf("want access.api-keys of api-key and label, and api-keys with groups of name, base-url, prefix, models, and keys of api-key and label")
	}
	s := State{Upstream: doc.APIKeys}
	if doc.Access != nil {
		s.ClientKeys = doc.Access.APIKeys
	}
	return s, s.Validate()
}

// checkLabelTypes refuses a label that is not a string, naming its key's
// position, before decoding would read a number or boolean as text.
func checkLabelTypes(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return nil // decoding reports the malformed document
	}
	check := func(keys *yaml.Node, position func(int) string) error {
		if keys == nil || keys.Kind != yaml.SequenceNode {
			return nil
		}
		for i, key := range keys.Content {
			label := child(key, "label")
			if label == nil || (label.Kind == yaml.ScalarNode && label.Tag == "!!null") {
				continue
			}
			// yaml.v3 writes the label "<<" unquoted and reads it back as a
			// merge key, which is still that text.
			if label.Kind != yaml.ScalarNode || (label.Tag != "!!str" && label.Tag != "!!merge") {
				return fmt.Errorf("%s: label must be a string", position(i))
			}
		}
		return nil
	}
	top := root.Content[0]
	if err := check(child(child(top, "access"), "api-keys"), func(i int) string {
		return fmt.Sprintf("access.api-keys: key %d", i+1)
	}); err != nil {
		return err
	}
	families := child(top, "api-keys")
	if families == nil || families.Kind != yaml.MappingNode {
		return nil
	}
	for f := 0; f+1 < len(families.Content); f += 2 {
		family, groups := families.Content[f].Value, families.Content[f+1]
		if groups.Kind != yaml.SequenceNode {
			continue
		}
		for g, group := range groups.Content {
			name := fmt.Sprintf("%d", g+1)
			if n := child(group, "name"); n != nil && n.Kind == yaml.ScalarNode {
				name = n.Value
			}
			if err := check(child(group, "keys"), func(i int) string {
				return fmt.Sprintf("api-keys.%s: group %s key %d", family, name, i+1)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks the client keys and each family's groups: no key is blank,
// padded with spaces, or repeated, every label is valid and unique among the
// client keys or within its family, and every group follows its family's
// rules with at least one key.
func (s State) Validate() error {
	if err := distinctKeys(keyValues(s.ClientKeys)); err != nil {
		return fmt.Errorf("access.api-keys: %w", err)
	}
	if err := upstream.ValidateKeyLabels(s.ClientKeys, func(i int) string { return fmt.Sprintf("key %d", i+1) }); err != nil {
		return fmt.Errorf("access.api-keys: %w", err)
	}
	for _, familyName := range s.families() {
		groups := s.Upstream[familyName]
		specs := make([]upstream.Spec, len(groups))
		for i, g := range groups {
			specs[i] = g.Spec
		}
		if err := upstream.ValidateSpecs(familyName, specs); err != nil {
			return fmt.Errorf("api-keys.%s: %w", familyName, err)
		}
		var familyKeys []upstream.Key
		var positions []string
		for _, g := range groups {
			if len(g.Keys) == 0 {
				return fmt.Errorf("api-keys.%s: group %s has no key", familyName, g.Name)
			}
			if err := distinctKeys(keyValues(g.Keys)); err != nil {
				return fmt.Errorf("api-keys.%s: group %s: %w", familyName, g.Name, err)
			}
			for i := range g.Keys {
				positions = append(positions, fmt.Sprintf("group %s key %d", g.Name, i+1))
			}
			familyKeys = append(familyKeys, g.Keys...)
		}
		if err := upstream.ValidateKeyLabels(familyKeys, func(i int) string { return positions[i] }); err != nil {
			return fmt.Errorf("api-keys.%s: %w", familyName, err)
		}
	}
	return nil
}

func keyValues(keys []upstream.Key) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.APIKey
	}
	return out
}

// distinctKeys checks that each key is non-blank, unpadded, and unique; the
// proxy compares keys as clients send them, without surrounding spaces.
func distinctKeys(keys []string) error {
	seen := map[string]bool{}
	for i, k := range keys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("key %d is empty", i+1)
		}
		if k != strings.TrimSpace(k) {
			return fmt.Errorf("key %d has surrounding spaces", i+1)
		}
		if seen[k] {
			return fmt.Errorf("key %d repeats an earlier key", i+1)
		}
		seen[k] = true
	}
	return nil
}

// families lists the state's families in a fixed order.
func (s State) families() []string {
	out := make([]string, 0, len(s.Upstream))
	for name := range s.Upstream {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// WithGroups returns a copy of the state whose family holds groups; no
// groups leaves the family out.
func (s State) WithGroups(familyName string, groups []upstream.Group) State {
	out := State{ClientKeys: s.ClientKeys, Upstream: make(map[string][]upstream.Group, len(s.Upstream)+1)}
	for name, g := range s.Upstream {
		out.Upstream[name] = g
	}
	delete(out.Upstream, familyName)
	if len(groups) > 0 {
		out.Upstream[familyName] = groups
	}
	return out
}

// WithClientKeys returns a copy of the state holding keys as its client keys.
func (s State) WithClientKeys(keys []upstream.Key) State {
	return State{ClientKeys: slices.Clone(keys), Upstream: s.Upstream}
}

func (s State) document() stateDocument {
	doc := stateDocument{APIKeys: s.Upstream}
	if len(s.ClientKeys) > 0 {
		doc.Access = &stateAccess{APIKeys: s.ClientKeys}
	}
	if len(doc.APIKeys) == 0 {
		doc.APIKeys = nil
	}
	return doc
}

// Encode renders the state as a state file.
func (s State) Encode() ([]byte, error) {
	return yaml.Marshal(s.document())
}

// WriteFile replaces the file at path with the state, readable by its owner
// alone. It writes a temporary file in the same directory and renames it into
// place, so a reader sees the old state or the new one.
func WriteFile(path string, s State) error {
	data, err := s.Encode()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if errClose := tmp.Close(); err == nil {
		err = errClose
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, errOpen := os.Open(dir); errOpen == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Merge adds the state's client keys and upstream groups to a v8
// configuration document. The document must declare neither: the state is
// their only source.
func Merge(document []byte, s State) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(document, &root); err != nil {
		return nil, fmt.Errorf("read the configuration document: %w", err)
	}
	if len(root.Content) == 0 {
		root.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	top := root.Content[0]
	if child(top, "api-keys") != nil {
		return nil, errors.New("the configuration document declares api-keys, which only the key state holds")
	}
	if access := child(top, "access"); access != nil && child(access, "api-keys") != nil {
		return nil, errors.New("the configuration document declares access.api-keys, which only the key state holds")
	}
	// The configuration takes client keys as plain values; their labels stay
	// in the key state.
	doc := mergedDocument{APIKeys: s.Upstream}
	if len(s.ClientKeys) > 0 {
		doc.Access = &mergedAccess{APIKeys: keyValues(s.ClientKeys)}
	}
	if len(doc.APIKeys) == 0 {
		doc.APIKeys = nil
	}
	var state yaml.Node
	if err := state.Encode(doc); err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(state.Content); i += 2 {
		key, value := state.Content[i], state.Content[i+1]
		if existing := child(top, key.Value); existing != nil && existing.Kind == yaml.MappingNode {
			existing.Content = append(existing.Content, value.Content...)
			continue
		}
		top.Content = append(top.Content, key, value)
	}
	return yaml.Marshal(&root)
}

// child returns the value of key in a mapping node, or nil.
func child(parent *yaml.Node, key string) *yaml.Node {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}
