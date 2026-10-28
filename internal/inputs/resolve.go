package inputs

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Process holds the process inputs of one run.
type Process struct {
	// Args are the command-line arguments after the program name.
	Args []string
	// Environ is the environment as KEY=value entries.
	Environ []string
}

// Files reads the overlay and protected files. ReadFile reports an absent
// file with an error matching fs.ErrNotExist.
type Files interface {
	ReadFile(name string) ([]byte, fs.FileMode, error)
}

// OSFiles reads files from the operating system.
type OSFiles struct{}

// ReadFile returns the file's contents and permission bits.
func (OSFiles) ReadFile(name string) ([]byte, fs.FileMode, error) {
	f, errOpen := os.Open(name)
	if errOpen != nil {
		return nil, 0, errOpen
	}
	defer func() { _ = f.Close() }()
	info, errStat := f.Stat()
	if errStat != nil {
		return nil, 0, errStat
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("not a regular file")
	}
	data, errRead := io.ReadAll(f)
	if errRead != nil {
		return nil, 0, errRead
	}
	return data, info.Mode().Perm(), nil
}

// Error lists every problem found while resolving inputs. Messages name
// inputs and files, never values.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "invalid inputs:\n  " + strings.Join(e.Problems, "\n  ")
}

// Effective holds the validated inputs of one run.
type Effective struct {
	Profile string
	schema  *Schema
	values  map[string]value
}

func (e *Effective) get(name, typ string) any {
	in, ok := e.schema.byName[name]
	if !ok || in.Shape.Type != typ && !(typ == TypeString && in.Shape.Type == TypePath) &&
		!(typ == TypeStringList && in.Shape.Type == TypePathList) {
		panic(fmt.Sprintf("inputs: %s is not a declared %s input", name, typ))
	}
	v, ok := e.values[name]
	if !ok {
		panic(fmt.Sprintf("inputs: %s does not apply to profile %s", name, e.Profile))
	}
	return v.raw()
}

// String returns a string or path input.
func (e *Effective) String(name string) string { return e.get(name, TypeString).(string) }

// Int returns an integer input.
func (e *Effective) Int(name string) int64 { return e.get(name, TypeInteger).(int64) }

// Bool returns a boolean input.
func (e *Effective) Bool(name string) bool { return e.get(name, TypeBoolean).(bool) }

// Strings returns a string list or path list input.
func (e *Effective) Strings(name string) []string { return e.get(name, TypeStringList).([]string) }

// resolver applies the layers, lowest first: schema default, overlay files,
// protected file, process input.
type resolver struct {
	schema   *Schema
	profile  string
	process  map[string]value
	files    map[string]value
	resolved map[string]value
	visiting map[string]bool
	problems []string
}

func (r *resolver) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

// Resolve validates the effective inputs of one run. It returns an error
// matching flag.ErrHelp when the command line asks for help.
func Resolve(s *Schema, p Process, files Files) (*Effective, error) {
	r := &resolver{
		schema:   s,
		process:  map[string]value{},
		files:    map[string]value{},
		resolved: map[string]value{},
		visiting: map[string]bool{},
	}
	if errHelp := r.readProcess(p); errHelp != nil {
		return nil, errHelp
	}
	if len(r.problems) > 0 {
		return nil, &Error{Problems: r.problems}
	}
	profile, ok := r.process[ProfileInput]
	if !ok {
		return nil, &Error{Problems: []string{ProfileInput + ": required; want one of " + strings.Join(s.Profiles, ", ")}}
	}
	r.profile = profile.v.(string)
	if !slices.Contains(s.Profiles, r.profile) {
		return nil, &Error{Problems: []string{ProfileInput + ": want one of " + strings.Join(s.Profiles, ", ")}}
	}
	for name := range r.process {
		if in := s.byName[name]; !in.AppliesTo(r.profile) && in.Flag != "" {
			r.problem("-%s: does not apply to profile %s", in.Flag, r.profile)
		}
	}
	if overlays, okOverlays := r.resolve(OverlayFilesInput); okOverlays {
		for _, path := range overlays.v.([]string) {
			r.readFile(files, path, Configuration)
		}
	}
	if protected, okProtected := r.resolve(ProtectedFileInput); okProtected {
		r.readFile(files, protected.v.(string), Protected)
	}
	for _, in := range s.Inputs {
		if in.AppliesTo(r.profile) {
			r.resolve(in.Name)
		}
	}
	r.checkCredentials()
	if len(r.problems) > 0 {
		sort.Strings(r.problems)
		return nil, &Error{Problems: slices.Compact(r.problems)}
	}
	return &Effective{Profile: r.profile, schema: s, values: r.resolved}, nil
}

// checkCredentials pairs each upstream group with its keys once both inputs
// resolved.
func (r *resolver) checkCredentials() {
	for _, in := range r.schema.Inputs {
		if in.Credentials == "" {
			continue
		}
		groups, okGroups := r.resolved[in.Name]
		credentials, okCredentials := r.resolved[in.Credentials]
		if !okGroups || !okCredentials {
			continue
		}
		for _, p := range checkCredentials(groups.v.([]UpstreamGroup), credentials.v.([]Credential)) {
			r.problem("%s: %s", in.Credentials, p)
		}
	}
}

// readProcess takes the declared environment variables and flags. Other
// environment variables are not inputs; an empty variable counts as unset.
// It returns flag.ErrHelp when the command line asks for help.
func (r *resolver) readProcess(p Process) error {
	env := map[string]string{}
	for _, entry := range p.Environ {
		name, text, found := strings.Cut(entry, "=")
		if _, seen := env[name]; found && !seen {
			env[name] = text
		}
	}
	flags := flag.NewFlagSet("CLIProxyAPI", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	for _, in := range r.schema.Inputs {
		switch {
		case in.Env != "":
			if text := env[in.Env]; text != "" {
				r.setProcess(in, text, in.Env)
			}
		case in.Flag != "":
			flags.Var(&flagValue{r: r, in: in}, in.Flag, in.Description)
		}
	}
	if errParse := flags.Parse(p.Args); errParse != nil {
		if errors.Is(errParse, flag.ErrHelp) {
			return flag.ErrHelp
		}
		r.problem("command line: %v", errParse)
		return nil
	}
	if flags.NArg() > 0 {
		r.problem("command line: unexpected argument %q", flags.Arg(0))
	}
	return nil
}

func (r *resolver) setProcess(in Input, text, source string) error {
	v, errValue := in.Shape.fromText(text)
	if errValue != nil {
		r.problem("%s: %v", source, errValue)
		return errValue
	}
	r.process[in.Name] = v
	return nil
}

type flagValue struct {
	r  *resolver
	in Input
}

func (f *flagValue) String() string { return "" }

func (f *flagValue) Set(text string) error {
	return f.r.setProcess(f.in, text, "-"+f.in.Flag)
}

func (f *flagValue) IsBoolFlag() bool { return f.in.Shape.Type == TypeBoolean }

// protectedLine is one line of the protected file: a secret name and its
// single-quoted value, which holds no quote or line break.
var protectedLine = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)='([^'\r\n]*)'$`)

// readFile reads one overlay or protected file. An absent file is an absent
// layer; an unreadable, malformed, or unprotected file is a problem.
func (r *resolver) readFile(files Files, path string, class Class) {
	data, mode, errRead := files.ReadFile(path)
	if errors.Is(errRead, fs.ErrNotExist) {
		return
	}
	if errRead != nil {
		r.problem("%s: unreadable: %v", path, errRead)
		return
	}
	if class == Protected && mode&0o077 != 0 {
		r.problem("%s: protected file must be accessible by its owner alone", path)
		return
	}
	if class == Protected {
		r.readProtected(path, string(data))
		return
	}
	var doc yaml.Node
	if errParse := yaml.Unmarshal(data, &doc); errParse != nil {
		r.problem("%s: malformed YAML", path)
		return
	}
	if len(doc.Content) == 0 {
		return
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		r.problem("%s: want a mapping of input keys", path)
		return
	}
	r.walk(path, "", root)
}

// readProtected reads the protected file's NAME='value' lines; blank lines
// and lines starting with # are ignored. Problems name lines, never values.
func (r *resolver) readProtected(path, text string) {
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := protectedLine.FindStringSubmatch(line)
		if match == nil {
			r.problem("%s: line %d is not NAME='value'", path, i+1)
			continue
		}
		name, text := match[1], match[2]
		in, declared := r.secretInput(name)
		switch {
		case !declared:
			r.problem("%s: %s is not a protected input", path, name)
		case seen[name]:
			r.problem("%s: %s is set twice", path, name)
		case !in.AppliesTo(r.profile):
			r.problem("%s: %s does not apply to profile %s", path, name, r.profile)
		default:
			seen[name] = true
			v, errValue := in.Shape.fromText(text)
			if errValue != nil {
				r.problem("%s: %s: %v", path, name, errValue)
				continue
			}
			r.files[in.Name] = v
		}
	}
}

func (r *resolver) secretInput(name string) (Input, bool) {
	for _, in := range r.schema.Inputs {
		if in.Secret == name {
			return in, true
		}
	}
	return Input{}, false
}

func (r *resolver) walk(path string, prefix string, node *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if prefix != "" {
			key = prefix + "." + key
		}
		child := node.Content[i+1]
		in, declared := r.keyInput(key)
		switch {
		case declared && in.Class == Protected:
			r.problem("%s: %s is a protected input and belongs in the protected file as %s", path, key, in.Secret)
		case declared:
			switch {
			case !in.AppliesTo(r.profile):
				r.problem("%s: %s does not apply to profile %s", path, key, r.profile)
			default:
				v, errValue := in.Shape.fromYAML(child)
				if errValue != nil {
					r.problem("%s: %s: %v", path, key, errValue)
					continue
				}
				r.files[in.Name] = v
			}
		case child.Kind == yaml.MappingNode && r.keyPrefix(key):
			r.walk(path, key, child)
		default:
			r.problem("%s: %s is not an input", path, key)
		}
	}
}

func (r *resolver) keyInput(key string) (Input, bool) {
	for _, in := range r.schema.Inputs {
		if in.Key == key {
			return in, true
		}
	}
	return Input{}, false
}

func (r *resolver) keyPrefix(prefix string) bool {
	for _, in := range r.schema.Inputs {
		if strings.HasPrefix(in.Key, prefix+".") {
			return true
		}
	}
	return false
}

// resolve returns the input's effective value, recording a problem when it
// is required and absent or invalid.
func (r *resolver) resolve(name string) (value, bool) {
	if v, done := r.resolved[name]; done {
		return v, true
	}
	in := r.schema.byName[name]
	if !in.AppliesTo(r.profile) {
		return value{}, false
	}
	if r.visiting[name] {
		r.problem("%s: default references itself", name)
		return value{}, false
	}
	r.visiting[name] = true
	defer delete(r.visiting, name)

	v, ok := r.process[name]
	if !ok {
		v, ok = r.files[name]
	}
	if !ok {
		v, ok = r.schema.defaults[name][r.profile]
		if ok {
			var missing []string
			for _, ref := range v.references() {
				if _, okRef := r.resolve(ref); !okRef {
					missing = append(missing, ref)
				}
			}
			if len(missing) > 0 {
				r.problem("%s: required; its default needs %s", r.label(in), strings.Join(missing, ", "))
				return value{}, false
			}
			v = v.expand(func(ref string) string { return r.resolved[ref].v.(string) })
		}
	}
	if !ok {
		r.problem("%s: required", r.label(in))
		return value{}, false
	}
	v = in.Shape.clean(v)
	if errValidate := in.Shape.validate(v); errValidate != nil {
		r.problem("%s: %v", r.label(in), errValidate)
		return value{}, false
	}
	r.resolved[name] = v
	return v, true
}

func (r *resolver) label(in *Input) string {
	if in.Flag != "" {
		return "-" + in.Flag
	}
	return in.Name
}
