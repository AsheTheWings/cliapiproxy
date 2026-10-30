// Package upstream defines the upstream groups of the API-key families: the
// providers a family's keys reach, the models each offers, and the rules a
// group must follow wherever it is declared.
package upstream

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// The API-key families. Each family's groups render under the
// configuration key api-keys.<family>.
const (
	FamilyOpenAICompatibility = "openai-compatibility"
	FamilyMeta                = "meta"
	FamilyCommandCode         = "commandcode"
	FamilyKiro                = "kiro"
)

// Spec is one upstream provider of an API-key family: where it serves and
// which models it offers.
type Spec struct {
	Name    string  `yaml:"name" json:"name"`
	BaseURL string  `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	Prefix  string  `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Models  []Model `yaml:"models,omitempty" json:"models,omitempty"`
}

// Model maps a client-visible alias to an upstream model.
type Model struct {
	Name             string    `yaml:"name" json:"name"`
	Alias            string    `yaml:"alias,omitempty" json:"alias,omitempty"`
	DisplayName      string    `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	MaxContextLength int       `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`
	InputModalities  []string  `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`
	Thinking         *Thinking `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

// Thinking lists the reasoning effort levels a model accepts.
type Thinking struct {
	Levels []string `yaml:"levels" json:"levels"`
}

// Key is one API key of a group.
type Key struct {
	APIKey string `yaml:"api-key" json:"api-key"`
}

// Group is a spec with its keys, the form the configuration's v8 api-keys
// layout takes.
type Group struct {
	Spec `yaml:",inline"`
	Keys []Key `yaml:"keys" json:"keys"`
}

// NamePattern is the form of group names and prefixes.
var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// family states what a family's groups must and may declare.
type family struct {
	requiresBaseURL bool
	requiresModels  bool
	inputModalities bool
	// accountCatalog families serve the models of each key's account from a
	// fixed endpoint, so their groups declare no base-url.
	accountCatalog bool
}

var families = map[string]family{
	FamilyOpenAICompatibility: {requiresBaseURL: true, requiresModels: true, inputModalities: true},
	FamilyMeta:                {},
	FamilyCommandCode:         {accountCatalog: true},
	FamilyKiro:                {accountCatalog: true},
}

// Families lists the API-key families in a fixed order.
func Families() []string {
	out := make([]string, 0, len(families))
	for name := range families {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// KnownFamily reports whether name is an API-key family.
func KnownFamily(name string) bool {
	_, known := families[name]
	return known
}

// ValidateSpecs checks a family's specs against its rules.
func ValidateSpecs(familyName string, specs []Spec) error {
	rules, known := families[familyName]
	if !known {
		return fmt.Errorf("unknown family %q", familyName)
	}
	seen := map[string]bool{}
	for i, g := range specs {
		if !NamePattern.MatchString(g.Name) {
			return fmt.Errorf("group %d: want a name of lowercase letters, digits, and hyphens", i+1)
		}
		label := "group " + g.Name
		if seen[g.Name] {
			return fmt.Errorf("%s is declared twice", label)
		}
		seen[g.Name] = true
		if g.BaseURL == "" && rules.requiresBaseURL {
			return fmt.Errorf("%s: base-url is required", label)
		}
		if rules.accountCatalog && g.BaseURL != "" {
			return fmt.Errorf("%s: the %s family takes no base-url; each key serves its account's models", label, familyName)
		}
		if g.BaseURL != "" {
			if u, errURL := url.Parse(g.BaseURL); errURL != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
				return fmt.Errorf("%s: base-url must be an http or https URL without credentials", label)
			}
		}
		if g.Prefix != "" && !NamePattern.MatchString(g.Prefix) {
			return fmt.Errorf("%s: want a prefix of lowercase letters, digits, and hyphens", label)
		}
		if len(g.Models) == 0 && rules.requiresModels {
			return fmt.Errorf("%s: models are required", label)
		}
		for j, m := range g.Models {
			if strings.TrimSpace(m.Name) == "" {
				return fmt.Errorf("%s: model %d has no name", label, j+1)
			}
			if m.MaxContextLength < 0 {
				return fmt.Errorf("%s: model %s: max-context-length must not be negative", label, m.Name)
			}
			if len(m.InputModalities) > 0 && !rules.inputModalities {
				return fmt.Errorf("%s: model %s: the %s family takes no input-modalities", label, m.Name, familyName)
			}
			if !nonEmptyItems(m.InputModalities) {
				return fmt.Errorf("%s: model %s: input-modalities must not be empty strings", label, m.Name)
			}
			if m.Thinking != nil && (len(m.Thinking.Levels) == 0 || !nonEmptyItems(m.Thinking.Levels)) {
				return fmt.Errorf("%s: model %s: thinking needs non-empty levels", label, m.Name)
			}
		}
	}
	return nil
}

func nonEmptyItems(items []string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			return false
		}
	}
	return true
}
