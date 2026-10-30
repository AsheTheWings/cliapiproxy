package inputs

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Upstream families an upstream-groups input can declare. Each family's
// groups render under the configuration key api-keys.<family>.
const (
	FamilyOpenAICompatibility = "openai-compatibility"
	FamilyMeta                = "meta"
	FamilyCommandCode         = "commandcode"
	FamilyKiro                = "kiro"
)

// UpstreamGroup is one upstream provider of an API-key family: where it
// serves and which models it offers. Its keys come from the group's
// credential-list input.
type UpstreamGroup struct {
	Name    string          `yaml:"name"`
	BaseURL string          `yaml:"base-url,omitempty"`
	Prefix  string          `yaml:"prefix,omitempty"`
	Models  []UpstreamModel `yaml:"models,omitempty"`
}

// UpstreamModel maps a client-visible alias to an upstream model.
type UpstreamModel struct {
	Name             string            `yaml:"name"`
	Alias            string            `yaml:"alias,omitempty"`
	DisplayName      string            `yaml:"display-name,omitempty"`
	MaxContextLength int               `yaml:"max-context-length,omitempty"`
	InputModalities  []string          `yaml:"input-modalities,omitempty"`
	Thinking         *UpstreamThinking `yaml:"thinking,omitempty"`
}

// UpstreamThinking lists the reasoning effort levels a model accepts.
type UpstreamThinking struct {
	Levels []string `yaml:"levels"`
}

// Credential is one API key of an upstream group.
type Credential struct {
	Group string
	Key   string
}

var groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

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

// groupsFromYAML decodes an overlay's group list. Unknown fields, keys
// included, are problems: a group's keys belong to the protected file.
func groupsFromYAML(node *yaml.Node) (value, error) {
	if node.Kind != yaml.SequenceNode {
		return value{}, fmt.Errorf("want a list of groups")
	}
	data, errEncode := yaml.Marshal(node)
	if errEncode != nil {
		return value{}, fmt.Errorf("want a list of groups")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var groups []UpstreamGroup
	if errDecode := decoder.Decode(&groups); errDecode != nil {
		return value{}, fmt.Errorf("want groups of name, prefix, and, where the family takes them, base-url and models; keys belong in the protected file")
	}
	if groups == nil {
		groups = []UpstreamGroup{}
	}
	return value{groups}, nil
}

// validateGroups checks the groups against their family's rules.
func validateGroups(familyName string, groups []UpstreamGroup) error {
	rules := families[familyName]
	seen := map[string]bool{}
	for i, g := range groups {
		if !groupNamePattern.MatchString(g.Name) {
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
		if g.Prefix != "" && !groupNamePattern.MatchString(g.Prefix) {
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

// credentialsFromText parses comma-separated group=key items; the empty
// text is no credentials. Errors name item positions, never keys.
func credentialsFromText(text string) (value, error) {
	out := []Credential{}
	if text == "" {
		return value{out}, nil
	}
	for i, item := range strings.Split(text, ",") {
		group, key, found := strings.Cut(item, "=")
		if !found || !groupNamePattern.MatchString(group) || key == "" {
			return value{}, fmt.Errorf("item %d: want group=key", i+1)
		}
		out = append(out, Credential{Group: group, Key: key})
	}
	return value{out}, nil
}

// checkCredentials reports the groups that have no credential. A credential
// of an undeclared group is unused, so keys can be delivered before the
// overlay that declares their group and stay after it is removed.
func checkCredentials(groups []UpstreamGroup, credentials []Credential) []string {
	var problems []string
	keyed := map[string]bool{}
	for _, c := range credentials {
		keyed[c.Group] = true
	}
	for _, g := range groups {
		if !keyed[g.Name] {
			problems = append(problems, fmt.Sprintf("group %s has no key", g.Name))
		}
	}
	return problems
}

// renderedGroup is a group in the configuration's v8 api-keys layout.
type renderedGroup struct {
	UpstreamGroup `yaml:",inline"`
	Keys          []renderedKey `yaml:"keys"`
}

type renderedKey struct {
	APIKey string `yaml:"api-key"`
}

func renderGroups(groups []UpstreamGroup, credentials []Credential) []renderedGroup {
	out := make([]renderedGroup, 0, len(groups))
	for _, g := range groups {
		r := renderedGroup{UpstreamGroup: g, Keys: []renderedKey{}}
		for _, c := range credentials {
			if c.Group == g.Name {
				r.Keys = append(r.Keys, renderedKey{APIKey: c.Key})
			}
		}
		out = append(out, r)
	}
	return out
}
