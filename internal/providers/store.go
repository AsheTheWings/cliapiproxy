// Package providers owns the provider state file: the upstream groups and
// keys the server keeps as application state, beside the groups its inputs
// declare. It builds the configuration the server runs with from both.
package providers

import (
	"errors"
	"fmt"
	"sync"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"github.com/AsheTheWings/cliapiproxy/internal/util"
)

// ErrInvalid marks a rejected change: the state it would produce breaks a
// group rule or conflicts with the inputs.
var ErrInvalid = errors.New("invalid provider groups")

// Store builds the effective configuration from the configuration document
// the inputs render and the provider state. It is safe for concurrent use.
type Store struct {
	path     string
	document []byte
	inputs   upstream.State

	mu    sync.Mutex
	state upstream.State
}

// New returns the store of the state file at path, whose current content is
// state; an empty path keeps no state. document is the configuration
// document of the server's inputs.
func New(path string, document, state []byte) (*Store, error) {
	inputs, err := upstream.DocumentGroups(document)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, document: document, inputs: inputs, state: upstream.State{}}
	if path == "" {
		return s, nil
	}
	parsed, err := upstream.ParseState(state)
	if err != nil {
		return nil, fmt.Errorf("provider state %s: %w", path, err)
	}
	if _, err = s.build(parsed); err != nil {
		return nil, fmt.Errorf("provider state %s: %w", path, err)
	}
	s.state = parsed
	return s, nil
}

// Enabled reports whether the store keeps a state file.
func (s *Store) Enabled() bool { return s != nil && s.path != "" }

// Config builds the configuration from the inputs and the current state.
func (s *Store) Config() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.build(s.state)
}

// Groups returns a family's groups: those the inputs declare, then those of
// the state.
func (s *Store) Groups(family string) (inputs, state []upstream.Group) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]upstream.Group(nil), s.inputs[family]...), append([]upstream.Group(nil), s.state[family]...)
}

// Replace makes groups the state's groups of family, writes the state file,
// and returns the configuration built from it. A change the rules or the
// inputs reject wraps ErrInvalid and leaves the state as it was.
func (s *Store) Replace(family string, groups []upstream.Group) (*config.Config, error) {
	if !s.Enabled() {
		return nil, errors.New("the server keeps no provider state")
	}
	if !upstream.KnownFamily(family) {
		return nil, fmt.Errorf("%w: unknown family %q", ErrInvalid, family)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.With(family, groups)
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	cfg, err := s.build(next)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err = upstream.WriteStateFile(s.path, next); err != nil {
		return nil, fmt.Errorf("write provider state: %w", err)
	}
	s.state = next
	return cfg, nil
}

// build parses the inputs' document with the state's groups added.
func (s *Store) build(state upstream.State) (*config.Config, error) {
	merged, err := upstream.MergeDocument(s.document, state)
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
