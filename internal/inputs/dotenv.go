package inputs

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// DotenvProfile is the profile whose defaults the generated .env lists.
const DotenvProfile = "development"

// RenderDotenv renders the environment inputs with their development
// defaults. The file is for reading; no process or tool loads it.
func (s *Schema) RenderDotenv() []byte {
	var b bytes.Buffer
	b.WriteString("# Generated from internal/inputs/schema.json by scripts/generate-env.sh; do not edit.\n")
	b.WriteString("# Development defaults of the environment inputs, for reading: no process or tool loads\n")
	b.WriteString("# this file. Flags and the keys of the overlay and protected files are in the schema.\n")
	for _, in := range s.Inputs {
		if in.Env == "" || !in.AppliesTo(DotenvProfile) {
			continue
		}
		b.WriteString("\n# " + in.Description + "\n")
		v, ok := s.defaults[in.Name][DotenvProfile]
		if !ok {
			fmt.Fprintf(&b, "# Required in %s.\n%s=\n", DotenvProfile, in.Env)
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", in.Env, dotenvText(in.Shape, v))
	}
	return b.Bytes()
}

// Usage describes the process inputs for a command-line help request.
func (s *Schema) Usage(program string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: %s [flags]\n\nFlags:\n", program)
	for _, in := range s.Inputs {
		if in.Flag != "" {
			fmt.Fprintf(&b, "  -%s\n        %s\n", in.Flag, in.Description)
		}
	}
	b.WriteString("\nEnvironment:\n")
	for _, in := range s.Inputs {
		if in.Env != "" {
			fmt.Fprintf(&b, "  %s\n        %s\n", in.Env, in.Description)
		}
	}
	b.WriteString("\nThe overlay and protected files hold the keyed inputs of internal/inputs/schema.json.\n")
	return b.String()
}

func dotenvText(sh Shape, v value) string {
	switch x := v.v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	case []string:
		if sh.Type == TypePathList {
			return strings.Join(x, string(filepath.ListSeparator))
		}
		return strings.Join(x, ",")
	}
	return ""
}
