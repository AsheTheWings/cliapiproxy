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
	"unicode"
	"unicode/utf8"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"gopkg.in/yaml.v3"
)

// State is the keys the server keeps: the client API keys that reach the
// proxy, with the name an operator gives each, and each API-key family's
// upstream groups with their keys. The state file holds it in the
// configuration's v8 layout, under access.api-keys and api-keys, with the
// names under access.api-key-names.
type State struct {
	ClientKeys []string `yaml:"-"`
	// ClientKeyNames holds the name of the client key at the same position,
	// empty for an unnamed key; nil names none.
	ClientKeyNames []string                    `yaml:"-"`
	Upstream       map[string][]upstream.Group `yaml:"-"`
}

// MaxClientKeyNameLength bounds a client key's name, in characters.
const MaxClientKeyNameLength = 64

type stateAccess struct {
	APIKeys     []string `yaml:"api-keys,omitempty"`
	APIKeyNames []string `yaml:"api-key-names,omitempty"`
}

type stateDocument struct {
	Access  *stateAccess                `yaml:"access,omitempty"`
	APIKeys map[string][]upstream.Group `yaml:"api-keys,omitempty"`
}

// Parse decodes and validates a state file; empty data is the empty state.
// Errors name groups and positions, never keys.
func Parse(data []byte) (State, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return State{}, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var doc stateDocument
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return State{}, fmt.Errorf("want access.api-keys, access.api-key-names, and api-keys with groups of name, base-url, prefix, models, and keys")
	}
	s := State{Upstream: doc.APIKeys}
	if doc.Access != nil {
		s.ClientKeys = doc.Access.APIKeys
		s.ClientKeyNames = doc.Access.APIKeyNames
	}
	return s, s.Validate()
}

// Validate checks the client keys and each family's groups: no key is empty
// or repeated, each name is one line of at most MaxClientKeyNameLength
// characters, and every group follows its family's rules with at least one
// key.
func (s State) Validate() error {
	if err := distinctKeys(s.ClientKeys); err != nil {
		return fmt.Errorf("access.api-keys: %w", err)
	}
	if s.ClientKeyNames != nil && len(s.ClientKeyNames) != len(s.ClientKeys) {
		return fmt.Errorf("access.api-key-names: %d names for %d keys", len(s.ClientKeyNames), len(s.ClientKeys))
	}
	for i, name := range s.ClientKeyNames {
		if err := validName(name); err != nil {
			return fmt.Errorf("access.api-key-names: name %d %w", i+1, err)
		}
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
		for _, g := range groups {
			if len(g.Keys) == 0 {
				return fmt.Errorf("api-keys.%s: group %s has no key", familyName, g.Name)
			}
			keys := make([]string, len(g.Keys))
			for i, k := range g.Keys {
				keys[i] = k.APIKey
			}
			if err := distinctKeys(keys); err != nil {
				return fmt.Errorf("api-keys.%s: group %s: %w", familyName, g.Name, err)
			}
		}
	}
	return nil
}

func distinctKeys(keys []string) error {
	seen := map[string]bool{}
	for i, k := range keys {
		if k == "" {
			return fmt.Errorf("key %d is empty", i+1)
		}
		if seen[k] {
			return fmt.Errorf("key %d repeats an earlier key", i+1)
		}
		seen[k] = true
	}
	return nil
}

func validName(name string) error {
	if name != strings.TrimSpace(name) {
		return errors.New("has surrounding spaces")
	}
	if utf8.RuneCountInString(name) > MaxClientKeyNameLength {
		return fmt.Errorf("is longer than %d characters", MaxClientKeyNameLength)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("holds a control character")
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
	out := State{ClientKeys: s.ClientKeys, ClientKeyNames: s.ClientKeyNames, Upstream: make(map[string][]upstream.Group, len(s.Upstream)+1)}
	for name, g := range s.Upstream {
		out.Upstream[name] = g
	}
	delete(out.Upstream, familyName)
	if len(groups) > 0 {
		out.Upstream[familyName] = groups
	}
	return out
}

// WithClientKeys returns a copy of the state holding keys as its client keys
// and names as their names; names without a non-empty name become nil.
func (s State) WithClientKeys(keys, names []string) State {
	if !slices.ContainsFunc(names, func(name string) bool { return name != "" }) {
		names = nil
	}
	return State{ClientKeys: slices.Clone(keys), ClientKeyNames: slices.Clone(names), Upstream: s.Upstream}
}

func (s State) document() stateDocument {
	doc := stateDocument{APIKeys: s.Upstream}
	if len(s.ClientKeys) > 0 {
		doc.Access = &stateAccess{APIKeys: s.ClientKeys, APIKeyNames: s.ClientKeyNames}
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
	// Names label keys for operators; the configuration has no field for them.
	doc := s.document()
	if doc.Access != nil {
		doc.Access = &stateAccess{APIKeys: doc.Access.APIKeys}
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
