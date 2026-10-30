// Package inputs declares every process input of the server and resolves the
// effective inputs of one run. The schema in schema.json owns each input's
// name, source, shape, class, and per-profile default; the entry point is the
// only caller, and everything downstream receives typed values.
package inputs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

//go:embed schema.json
var schemaJSON []byte

// Class separates configuration from protected inputs, whose values grant
// access or identity and are never printed.
type Class string

const (
	Configuration Class = "configuration"
	Protected     Class = "protected"
)

// ProfileInput names the input that selects the profile. It has no default.
const ProfileInput = "CLIPROXY_PROFILE"

// OverlayFilesInput and ProtectedFileInput name the files of the two file layers.
const (
	OverlayFilesInput  = "CLIPROXY_OVERLAY_FILES"
	ProtectedFileInput = "CLIPROXY_PROTECTED_FILE"
)

// Shape types.
const (
	TypeString     = "string"
	TypeInteger    = "integer"
	TypeBoolean    = "boolean"
	TypeStringList = "string-list"
	TypePath       = "path"
	TypePathList   = "path-list"
)

// Shape constrains an input's value.
type Shape struct {
	Type      string   `json:"type"`
	Enum      []string `json:"enum,omitempty"`
	MinLength int      `json:"minLength,omitempty"`
	MinItems  int      `json:"minItems,omitempty"`
	Minimum   *int64   `json:"minimum,omitempty"`
	Maximum   *int64   `json:"maximum,omitempty"`
}

// Input declares one process input. Exactly one of Env, Flag, and Key names
// its source: an environment variable, a command-line flag, or a key, which
// a configuration input takes from the overlay files. A protected input
// takes its key's value from the protected file's line named Secret.
type Input struct {
	Name        string         `json:"name"`
	Env         string         `json:"env,omitempty"`
	Flag        string         `json:"flag,omitempty"`
	Key         string         `json:"key,omitempty"`
	Secret      string         `json:"secret,omitempty"`
	Description string         `json:"description"`
	Class       Class          `json:"class"`
	Shape       Shape          `json:"shape"`
	Profiles    []string       `json:"profiles,omitempty"`
	Defaults    map[string]any `json:"defaults"`
	// EntryPointOnly keeps a keyed input out of the configuration document;
	// the entry point consumes it directly.
	EntryPointOnly bool `json:"entryPointOnly,omitempty"`
}

// AppliesTo reports whether the input exists in the profile.
func (in Input) AppliesTo(profile string) bool {
	return len(in.Profiles) == 0 || slices.Contains(in.Profiles, profile)
}

// Schema is the parsed schema with each default already checked against its shape.
type Schema struct {
	Profiles []string `json:"profiles"`
	Inputs   []Input  `json:"inputs"`

	byName   map[string]*Input
	defaults map[string]map[string]value
}

// Input returns the declared input with the name.
func (s *Schema) Input(name string) (Input, bool) {
	in, ok := s.byName[name]
	if !ok {
		return Input{}, false
	}
	return *in, true
}

// Default returns the input's default for the profile, references unexpanded.
func (s *Schema) Default(name, profile string) (any, bool) {
	v, ok := s.defaults[name][profile]
	if !ok {
		return nil, false
	}
	return v.raw(), true
}

var (
	envNamePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	flagNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	keyPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)
	referencePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// Load parses the embedded schema.
func Load() (*Schema, error) {
	return Parse(schemaJSON)
}

// Parse parses and checks a schema document.
func Parse(data []byte) (*Schema, error) {
	var s Schema
	if errUnmarshal := json.Unmarshal(data, &s); errUnmarshal != nil {
		return nil, fmt.Errorf("inputs schema: %w", errUnmarshal)
	}
	if errCheck := s.check(); errCheck != nil {
		return nil, fmt.Errorf("inputs schema: %w", errCheck)
	}
	return &s, nil
}

func (s *Schema) check() error {
	if len(s.Profiles) == 0 {
		return fmt.Errorf("no profiles")
	}
	s.byName = make(map[string]*Input, len(s.Inputs))
	s.defaults = make(map[string]map[string]value, len(s.Inputs))
	sources := map[string]string{}
	for i := range s.Inputs {
		in := &s.Inputs[i]
		if _, dup := s.byName[in.Name]; dup || in.Name == "" {
			return fmt.Errorf("input %q is unnamed or declared twice", in.Name)
		}
		s.byName[in.Name] = in
		source, errSource := in.source()
		if errSource != nil {
			return errSource
		}
		if other, dup := sources[source]; dup {
			return fmt.Errorf("inputs %q and %q share the source %s", other, in.Name, source)
		}
		sources[source] = in.Name
		if in.Secret != "" {
			if other, dup := sources["secret "+in.Secret]; dup {
				return fmt.Errorf("inputs %q and %q share the secret name %s", other, in.Name, in.Secret)
			}
			sources["secret "+in.Secret] = in.Name
		}
		if strings.TrimSpace(in.Description) == "" {
			return fmt.Errorf("input %q has no description", in.Name)
		}
		if in.Class != Configuration && in.Class != Protected {
			return fmt.Errorf("input %q has unknown class %q", in.Name, in.Class)
		}
		if in.EntryPointOnly && in.Key == "" {
			return fmt.Errorf("input %q: entryPointOnly applies only to keyed inputs", in.Name)
		}
		if in.Class == Protected && (in.Key == "" || !envNamePattern.MatchString(in.Secret)) {
			return fmt.Errorf("protected input %q must come from the protected file: declare its key and secret name", in.Name)
		}
		if in.Class != Protected && in.Secret != "" {
			return fmt.Errorf("input %q: only protected inputs have a secret name", in.Name)
		}
		if errShape := in.Shape.check(); errShape != nil {
			return fmt.Errorf("input %q: %w", in.Name, errShape)
		}
		for _, p := range in.Profiles {
			if !slices.Contains(s.Profiles, p) {
				return fmt.Errorf("input %q names unknown profile %q", in.Name, p)
			}
		}
		if in.Defaults == nil {
			return fmt.Errorf("input %q declares no defaults object", in.Name)
		}
		s.defaults[in.Name] = map[string]value{}
		for profile, raw := range in.Defaults {
			if !slices.Contains(s.Profiles, profile) || !in.AppliesTo(profile) {
				return fmt.Errorf("input %q has a default for profile %q it does not apply to", in.Name, profile)
			}
			if in.Class == Protected && !emptyDefault(raw) {
				return fmt.Errorf("protected input %q may default only to empty", in.Name)
			}
			v, errValue := in.Shape.fromJSON(raw)
			if errValue != nil {
				return fmt.Errorf("input %q default for %s: %w", in.Name, profile, errValue)
			}
			s.defaults[in.Name][profile] = v
		}
	}
	for _, in := range s.Inputs {
		for _, v := range s.defaults[in.Name] {
			for _, ref := range v.references() {
				target, ok := s.byName[ref]
				if !ok || target.Env == "" || (target.Shape.Type != TypePath && target.Shape.Type != TypeString) {
					return fmt.Errorf("input %q default references %q, which is not a string or path environment input", in.Name, ref)
				}
			}
		}
	}
	profile, ok := s.byName[ProfileInput]
	if !ok || profile.Env != ProfileInput || len(profile.Defaults) != 0 || !slices.Equal(profile.Shape.Enum, s.Profiles) {
		return fmt.Errorf("%s must be an environment input without defaults whose enum lists the profiles", ProfileInput)
	}
	for _, name := range []string{OverlayFilesInput, ProtectedFileInput} {
		if in, declared := s.byName[name]; !declared || in.Env == "" {
			return fmt.Errorf("%s must be an environment input", name)
		}
	}
	return nil
}

func emptyDefault(raw any) bool {
	if list, ok := raw.([]any); ok {
		return len(list) == 0
	}
	return raw == ""
}

func (in Input) source() (string, error) {
	var sources []string
	if in.Env != "" {
		if !envNamePattern.MatchString(in.Env) {
			return "", fmt.Errorf("input %q has invalid environment name %q", in.Name, in.Env)
		}
		sources = append(sources, "env "+in.Env)
	}
	if in.Flag != "" {
		if !flagNamePattern.MatchString(in.Flag) {
			return "", fmt.Errorf("input %q has invalid flag name %q", in.Name, in.Flag)
		}
		sources = append(sources, "flag -"+in.Flag)
	}
	if in.Key != "" {
		if !keyPattern.MatchString(in.Key) {
			return "", fmt.Errorf("input %q has invalid key %q", in.Name, in.Key)
		}
		sources = append(sources, "key "+in.Key)
	}
	if len(sources) != 1 {
		return "", fmt.Errorf("input %q must declare exactly one of env, flag, and key", in.Name)
	}
	return sources[0], nil
}

func (sh Shape) check() error {
	switch sh.Type {
	case TypeString, TypeStringList:
	case TypeInteger:
		if sh.Minimum != nil && sh.Maximum != nil && *sh.Minimum > *sh.Maximum {
			return fmt.Errorf("minimum exceeds maximum")
		}
	case TypeBoolean, TypePath, TypePathList:
	default:
		return fmt.Errorf("unknown shape type %q", sh.Type)
	}
	if len(sh.Enum) > 0 && sh.Type != TypeString {
		return fmt.Errorf("enum applies only to strings")
	}
	if (sh.Minimum != nil || sh.Maximum != nil) && sh.Type != TypeInteger {
		return fmt.Errorf("minimum and maximum apply only to integers")
	}
	if sh.MinItems > 0 && sh.Type != TypeStringList && sh.Type != TypePathList {
		return fmt.Errorf("minItems applies only to lists")
	}
	if sh.MinLength > 0 && sh.Type != TypeString && sh.Type != TypeStringList {
		return fmt.Errorf("minLength applies only to strings")
	}
	return nil
}
