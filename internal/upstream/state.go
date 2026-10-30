package upstream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// State is the upstream groups the server keeps as application state, by
// family, with their keys. A provider state file holds it under api-keys,
// in the configuration's v8 layout.
type State map[string][]Group

type stateDocument struct {
	APIKeys map[string][]Group `yaml:"api-keys"`
}

// ParseState decodes and validates a provider state file; empty data is the
// empty state. Errors name groups and positions, never keys.
func ParseState(data []byte) (State, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return State{}, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var doc stateDocument
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("want api-keys with groups of name, base-url, prefix, models, and keys")
	}
	state := State(doc.APIKeys)
	if state == nil {
		state = State{}
	}
	return state, state.Validate()
}

// Validate checks each family's groups against its rules; every group has at
// least one key, and no key is empty or repeated within its group.
func (s State) Validate() error {
	for _, familyName := range s.families() {
		groups := s[familyName]
		specs := make([]Spec, len(groups))
		for i, g := range groups {
			specs[i] = g.Spec
		}
		if err := ValidateSpecs(familyName, specs); err != nil {
			return fmt.Errorf("api-keys.%s: %w", familyName, err)
		}
		for _, g := range groups {
			if len(g.Keys) == 0 {
				return fmt.Errorf("api-keys.%s: group %s has no key", familyName, g.Name)
			}
			seen := map[string]bool{}
			for i, k := range g.Keys {
				if k.APIKey == "" {
					return fmt.Errorf("api-keys.%s: group %s: key %d is empty", familyName, g.Name, i+1)
				}
				if seen[k.APIKey] {
					return fmt.Errorf("api-keys.%s: group %s: key %d repeats an earlier key", familyName, g.Name, i+1)
				}
				seen[k.APIKey] = true
			}
		}
	}
	return nil
}

// families lists the state's families in a fixed order.
func (s State) families() []string {
	out := make([]string, 0, len(s))
	for name := range s {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// With returns a copy of the state whose family holds groups; no groups
// leaves the family out.
func (s State) With(familyName string, groups []Group) State {
	out := make(State, len(s)+1)
	for name, g := range s {
		out[name] = g
	}
	delete(out, familyName)
	if len(groups) > 0 {
		out[familyName] = groups
	}
	return out
}

// Encode renders the state as a provider state file.
func (s State) Encode() ([]byte, error) {
	return yaml.Marshal(stateDocument{APIKeys: s})
}

// WriteStateFile replaces the file at path with the state, readable by its
// owner alone. It writes a temporary file in the same directory and renames
// it into place, so a reader sees the old state or the new one.
func WriteStateFile(path string, s State) error {
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

// DocumentGroups returns the groups a v8 configuration document declares
// under api-keys, by family.
func DocumentGroups(document []byte) (State, error) {
	var doc struct {
		APIKeys map[string][]Group `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("read the configuration's api-keys: %w", err)
	}
	if doc.APIKeys == nil {
		return State{}, nil
	}
	return State(doc.APIKeys), nil
}

// MergeDocument appends the state's groups to a v8 configuration document's
// api-keys. A state group named like a group the document declares in the
// same family is a conflict.
func MergeDocument(document []byte, s State) ([]byte, error) {
	if len(s) == 0 {
		return document, nil
	}
	declared, err := DocumentGroups(document)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err = yaml.Unmarshal(document, &root); err != nil {
		return nil, fmt.Errorf("read the configuration document: %w", err)
	}
	if len(root.Content) == 0 {
		root.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	apiKeys := mappingChild(root.Content[0], "api-keys")
	for _, familyName := range s.families() {
		for _, g := range s[familyName] {
			if slices.ContainsFunc(declared[familyName], func(d Group) bool { return d.Name == g.Name }) {
				return nil, fmt.Errorf("api-keys.%s: group %s is declared by the server's inputs", familyName, g.Name)
			}
		}
		var groups yaml.Node
		if err = groups.Encode(s[familyName]); err != nil {
			return nil, err
		}
		list := mappingChild(apiKeys, familyName)
		if list.Kind != yaml.SequenceNode {
			*list = yaml.Node{Kind: yaml.SequenceNode}
		}
		list.Content = append(list.Content, groups.Content...)
	}
	return yaml.Marshal(&root)
}

// mappingChild returns the value of key in a mapping node, adding an empty
// mapping when the key is absent.
func mappingChild(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, child)
	return child
}
