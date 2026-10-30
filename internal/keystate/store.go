// Package keystate owns the key state file: the client API keys and the
// upstream groups with their keys, which the server keeps as application
// state and the management API changes. It builds the configuration the
// server runs with from the inputs' document and that state.
package keystate

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"github.com/AsheTheWings/cliapiproxy/internal/util"
)

// ErrInvalid marks a rejected change: the state it would produce breaks a
// rule of the client keys or of a family's groups.
var ErrInvalid = errors.New("invalid keys")

// Store builds the effective configuration from the configuration document
// the inputs render and the key state. It is safe for concurrent use.
type Store struct {
	path     string
	document []byte

	mu    sync.Mutex
	state State
}

// New returns the store of the state file at path, whose current content is
// state. document is the configuration document of the server's inputs.
func New(path string, document, state []byte) (*Store, error) {
	parsed, err := Parse(state)
	if err != nil {
		return nil, fmt.Errorf("key state %s: %w", path, err)
	}
	s := &Store{path: path, document: document, state: parsed}
	if _, err = s.build(parsed); err != nil {
		return nil, fmt.Errorf("key state %s: %w", path, err)
	}
	return s, nil
}

// Config builds the configuration from the inputs and the current state.
func (s *Store) Config() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.build(s.state)
}

// Groups returns a family's groups.
func (s *Store) Groups(family string) []upstream.Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.Upstream[family])
}

// ClientKeys returns the client API keys.
func (s *Store) ClientKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.ClientKeys)
}

// ClientKeyNames returns the name of each client key, empty for an unnamed
// key, or nil when no key has a name.
func (s *Store) ClientKeyNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.ClientKeyNames)
}

// ReplaceGroups makes groups a family's groups, writes the state file, and
// returns the configuration built from it. A change the rules reject wraps
// ErrInvalid and leaves the state as it was.
func (s *Store) ReplaceGroups(family string, groups []upstream.Group) (*config.Config, error) {
	if !upstream.KnownFamily(family) {
		return nil, fmt.Errorf("%w: unknown family %q", ErrInvalid, family)
	}
	return s.replace(func(st State) State { return st.WithGroups(family, groups) })
}

// ReplaceClientKeys makes keys the client API keys, named by names at the
// same positions, writes the state file, and returns the configuration built
// from it. No keys refuses every request to the proxy API.
func (s *Store) ReplaceClientKeys(keys, names []string) (*config.Config, error) {
	return s.replace(func(st State) State { return st.WithClientKeys(keys, names) })
}

func (s *Store) replace(change func(State) State) (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := change(s.state)
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	cfg, err := s.build(next)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err = WriteFile(s.path, next); err != nil {
		return nil, fmt.Errorf("write key state: %w", err)
	}
	s.state = next
	return cfg, nil
}

// build parses the inputs' document with the state's keys added.
func (s *Store) build(state State) (*config.Config, error) {
	merged, err := Merge(s.document, state)
	if err != nil {
		return nil, err
	}
	cfg, err := config.ParseConfigBytes(merged)
	if err != nil {
		return nil, fmt.Errorf("parse configuration: %w", err)
	}
	authDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return nil, err
	}
	cfg.AuthDir = authDir
	return cfg, nil
}
