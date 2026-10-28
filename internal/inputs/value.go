package inputs

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// value holds one typed input value: a string (string and path shapes),
// an int64, a bool, or a []string (list shapes).
type value struct {
	v any
}

func (v value) raw() any {
	if list, ok := v.v.([]string); ok {
		return slices.Clone(list)
	}
	return v.v
}

// references lists the inputs that ${NAME} references in the value name.
func (v value) references() []string {
	var out []string
	collect := func(s string) {
		for _, m := range referencePattern.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
	}
	switch x := v.v.(type) {
	case string:
		collect(x)
	case []string:
		for _, s := range x {
			collect(s)
		}
	}
	return out
}

// expand replaces each reference with lookup's value.
func (v value) expand(lookup func(string) string) value {
	replace := func(s string) string {
		return referencePattern.ReplaceAllStringFunc(s, func(m string) string {
			return lookup(referencePattern.FindStringSubmatch(m)[1])
		})
	}
	switch x := v.v.(type) {
	case string:
		return value{replace(x)}
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = replace(s)
		}
		return value{out}
	}
	return v
}

// fromJSON converts a schema default.
func (sh Shape) fromJSON(raw any) (value, error) {
	switch sh.Type {
	case TypeString, TypePath:
		s, ok := raw.(string)
		if !ok {
			return value{}, fmt.Errorf("want a string")
		}
		return value{s}, nil
	case TypeInteger:
		f, ok := raw.(float64)
		if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return value{}, fmt.Errorf("want an integer")
		}
		return value{int64(f)}, nil
	case TypeBoolean:
		b, ok := raw.(bool)
		if !ok {
			return value{}, fmt.Errorf("want a boolean")
		}
		return value{b}, nil
	case TypeStringList, TypePathList:
		items, ok := raw.([]any)
		if !ok {
			return value{}, fmt.Errorf("want a list")
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, okItem := item.(string)
			if !okItem {
				return value{}, fmt.Errorf("want a list of strings")
			}
			out = append(out, s)
		}
		return value{out}, nil
	}
	return value{}, fmt.Errorf("unknown shape type %q", sh.Type)
}

// fromYAML converts a value of an overlay or protected file. Scalars must
// already have the shape's YAML type: a quoted "8317" is not an integer.
func (sh Shape) fromYAML(node *yaml.Node) (value, error) {
	switch sh.Type {
	case TypeStringList, TypePathList:
		if node.Kind != yaml.SequenceNode {
			return value{}, fmt.Errorf("want a list")
		}
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return value{}, fmt.Errorf("want a list of strings")
			}
			out = append(out, item.Value)
		}
		return value{out}, nil
	}
	if node.Kind != yaml.ScalarNode {
		return value{}, fmt.Errorf("want a scalar")
	}
	switch sh.Type {
	case TypeString, TypePath:
		if node.Tag != "!!str" {
			return value{}, fmt.Errorf("want a string")
		}
		return value{node.Value}, nil
	case TypeInteger:
		var i int64
		if node.Tag != "!!int" || node.Decode(&i) != nil {
			return value{}, fmt.Errorf("want an integer")
		}
		return value{i}, nil
	case TypeBoolean:
		var b bool
		if node.Tag != "!!bool" || node.Decode(&b) != nil {
			return value{}, fmt.Errorf("want a boolean")
		}
		return value{b}, nil
	}
	return value{}, fmt.Errorf("unknown shape type %q", sh.Type)
}

// fromText converts an environment variable or flag value. A string list is
// comma-separated and a path list colon-separated.
func (sh Shape) fromText(text string) (value, error) {
	switch sh.Type {
	case TypeString, TypePath:
		return value{text}, nil
	case TypeInteger:
		i, errParse := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if errParse != nil {
			return value{}, fmt.Errorf("want an integer")
		}
		return value{i}, nil
	case TypeBoolean:
		b, errParse := strconv.ParseBool(strings.TrimSpace(text))
		if errParse != nil {
			return value{}, fmt.Errorf("want true or false")
		}
		return value{b}, nil
	case TypeStringList:
		return value{strings.Split(text, ",")}, nil
	case TypePathList:
		return value{filepath.SplitList(text)}, nil
	}
	return value{}, fmt.Errorf("unknown shape type %q", sh.Type)
}

// validate checks an expanded value against the shape's constraints.
func (sh Shape) validate(v value) error {
	switch x := v.v.(type) {
	case string:
		if len(x) < sh.MinLength {
			return fmt.Errorf("want at least %d characters", sh.MinLength)
		}
		if len(sh.Enum) > 0 && !slices.Contains(sh.Enum, x) {
			return fmt.Errorf("want one of %s", strings.Join(sh.Enum, ", "))
		}
		if sh.Type == TypePath {
			return checkPath(x)
		}
	case int64:
		if sh.Minimum != nil && x < *sh.Minimum {
			return fmt.Errorf("want at least %d", *sh.Minimum)
		}
		if sh.Maximum != nil && x > *sh.Maximum {
			return fmt.Errorf("want at most %d", *sh.Maximum)
		}
	case []string:
		if len(x) < sh.MinItems {
			return fmt.Errorf("want at least %d items", sh.MinItems)
		}
		for _, item := range x {
			if len(item) < sh.MinLength {
				return fmt.Errorf("want items of at least %d characters", sh.MinLength)
			}
			if sh.Type == TypePathList {
				if errPath := checkPath(item); errPath != nil {
					return errPath
				}
			}
		}
	}
	return nil
}

func checkPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("want an absolute path")
	}
	return nil
}

// clean normalizes path values once their references are expanded.
func (sh Shape) clean(v value) value {
	switch x := v.v.(type) {
	case string:
		if sh.Type == TypePath && x != "" {
			return value{filepath.Clean(x)}
		}
	case []string:
		if sh.Type == TypePathList {
			out := make([]string, len(x))
			for i, item := range x {
				out[i] = filepath.Clean(item)
			}
			return value{out}
		}
	}
	return v
}
