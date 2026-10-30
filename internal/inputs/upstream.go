package inputs

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"gopkg.in/yaml.v3"
)

// Credential is one API key of an upstream group.
type Credential struct {
	Group string
	Key   string
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
	var groups []upstream.Spec
	if errDecode := decoder.Decode(&groups); errDecode != nil {
		return value{}, fmt.Errorf("want groups of name, prefix, and, where the family takes them, base-url and models; keys belong in the protected file")
	}
	if groups == nil {
		groups = []upstream.Spec{}
	}
	return value{groups}, nil
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
		if !found || !upstream.NamePattern.MatchString(group) || key == "" {
			return value{}, fmt.Errorf("item %d: want group=key", i+1)
		}
		out = append(out, Credential{Group: group, Key: key})
	}
	return value{out}, nil
}

// checkCredentials reports the groups that have no credential. A credential
// of an undeclared group is unused, so keys can be delivered before the
// overlay that declares their group and stay after it is removed.
func checkCredentials(groups []upstream.Spec, credentials []Credential) []string {
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

// renderGroups pairs each group with the credentials of its name.
func renderGroups(groups []upstream.Spec, credentials []Credential) []upstream.Group {
	out := make([]upstream.Group, 0, len(groups))
	for _, g := range groups {
		r := upstream.Group{Spec: g, Keys: []upstream.Key{}}
		for _, c := range credentials {
			if c.Group == g.Name {
				r.Keys = append(r.Keys, upstream.Key{APIKey: c.Key})
			}
		}
		out = append(out, r)
	}
	return out
}
