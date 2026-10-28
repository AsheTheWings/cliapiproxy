package inputs

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigVersion is the configuration layout the document uses.
const ConfigVersion = 8

// ConfigDocument renders the effective keyed inputs as a configuration
// document in the v8 layout, for the configuration parser to read in memory.
// Keys the schema does not declare keep the parser's defaults.
func (e *Effective) ConfigDocument() ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	appendScalar(root, "config-version", ConfigVersion)
	for _, in := range e.schema.Inputs {
		v, ok := e.values[in.Name]
		if in.Key == "" || !ok {
			continue
		}
		parts := strings.Split(in.Key, ".")
		parent := root
		for _, part := range parts[:len(parts)-1] {
			parent = mappingChild(parent, part)
		}
		appendScalar(parent, parts[len(parts)-1], v.raw())
	}
	return yaml.Marshal(root)
}

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

func appendScalar(parent *yaml.Node, key string, v any) {
	var node yaml.Node
	// Encoding a string, integer, boolean, or string list cannot fail.
	_ = node.Encode(v)
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &node)
}
