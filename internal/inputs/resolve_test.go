//go:build unit

package inputs

import (
	"errors"
	"flag"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// memFiles serves overlay and protected files from memory.
type memFiles map[string]memFile

type memFile struct {
	data string
	mode fs.FileMode
	err  error
}

func (m memFiles) ReadFile(name string) ([]byte, fs.FileMode, error) {
	f, ok := m[name]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	return []byte(f.data), f.mode, nil
}

const protectedPath = "/run/cliapiproxy/protected.env"

func mustSchema(t *testing.T) *Schema {
	t.Helper()
	s, errLoad := Load()
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	return s
}

func production(env ...string) []string {
	return append([]string{"CLIPROXY_PROFILE=production", "CLIPROXY_PROTECTED_FILE=" + protectedPath,
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318", "CLIPROXY_DEPLOYMENT_ENVIRONMENT=prod"}, env...)
}

func validProtected() memFile {
	return memFile{data: "# client keys\nCLIPROXY_API_KEYS='client-key-1'\n\n", mode: 0o600}
}

func problems(t *testing.T, err error) []string {
	t.Helper()
	var inputsErr *Error
	if !errors.As(err, &inputsErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	return inputsErr.Problems
}

func wantProblem(t *testing.T, err error, fragment string) {
	t.Helper()
	for _, p := range problems(t, err) {
		if strings.Contains(p, fragment) {
			return
		}
	}
	t.Fatalf("problems %q lack %q", problems(t, err), fragment)
}

func TestEmbeddedSchemaParses(t *testing.T) {
	s := mustSchema(t)
	if !slices.Equal(s.Profiles, []string{"development", "production"}) {
		t.Fatalf("profiles = %v", s.Profiles)
	}
}

func TestProfileIsRequired(t *testing.T) {
	_, err := Resolve(mustSchema(t), Process{}, memFiles{})
	wantProblem(t, err, "CLIPROXY_PROFILE: required")
	_, err = Resolve(mustSchema(t), Process{Environ: []string{"CLIPROXY_PROFILE=staging"}}, memFiles{})
	wantProblem(t, err, "CLIPROXY_PROFILE: want one of development, production")
}

func TestProductionRequiresWhatItOwns(t *testing.T) {
	_, err := Resolve(mustSchema(t), Process{Environ: []string{"CLIPROXY_PROFILE=production"}}, memFiles{})
	for _, name := range []string{"CLIPROXY_PROTECTED_FILE: required", "server.port: required", "oauth.auth-dir: required", "access.api-keys: required",
		"OTEL_EXPORTER_OTLP_ENDPOINT: required", "CLIPROXY_DEPLOYMENT_ENVIRONMENT: required"} {
		wantProblem(t, err, name)
	}
}

func TestLaterOverlayOverridesEarlierAndDefaults(t *testing.T) {
	files := memFiles{
		protectedPath: validProtected(),
		"/etc/a.yaml": {data: "server:\n  port: 9000\n  host: 0.0.0.0\noauth:\n  auth-dir: /var/lib/a\n", mode: 0o644},
		"/etc/b.yaml": {data: "server: {port: 9001}\n", mode: 0o644},
	}
	eff, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml:/etc/missing.yaml:/etc/b.yaml")}, files)
	if err != nil {
		t.Fatal(err)
	}
	if got := eff.Int("server.port"); got != 9001 {
		t.Fatalf("server.port = %d, want the later overlay's 9001", got)
	}
	if got := eff.String("server.host"); got != "0.0.0.0" {
		t.Fatalf("server.host = %q, want the overlay's value", got)
	}
	if got := eff.String("routing.strategy"); got != "round-robin" {
		t.Fatalf("routing.strategy = %q, want the default", got)
	}
	if got := eff.Strings("access.api-keys"); !slices.Equal(got, []string{"client-key-1"}) {
		t.Fatalf("access.api-keys = %v", got)
	}
}

func TestProcessInputOverridesDefault(t *testing.T) {
	eff, err := Resolve(mustSchema(t), Process{
		Args:    []string{"-oauth-callback-port=1455", "-no-browser"},
		Environ: []string{"CLIPROXY_PROFILE=development", "HOME=/home/u", "XDG_RUNTIME_DIR=/run/user/1", "CLIPROXY_PROTECTED_FILE=/srv/p.yaml"},
	}, memFiles{"/srv/p.yaml": validProtected()})
	if err != nil {
		t.Fatal(err)
	}
	if eff.String("CLIPROXY_PROTECTED_FILE") != "/srv/p.yaml" || eff.Int("oauth-callback-port") != 1455 || !eff.Bool("no-browser") {
		t.Fatal("process inputs did not override the defaults")
	}
	if eff.Bool("codex-login") {
		t.Fatal("an unset flag lost its default")
	}
}

func TestDevelopmentDefaultsDeriveFromXDG(t *testing.T) {
	files := memFiles{"/run/user/1/cliapiproxy/protected.env": validProtected()}
	eff, err := Resolve(mustSchema(t), Process{Environ: []string{"CLIPROXY_PROFILE=development", "HOME=/home/u/", "XDG_RUNTIME_DIR=/run/user/1"}}, files)
	if err != nil {
		t.Fatal(err)
	}
	if got := eff.String("oauth.auth-dir"); got != "/home/u/.local/state/cliapiproxy/auths" {
		t.Fatalf("oauth.auth-dir = %q", got)
	}
	if got := eff.Int("server.port"); got != 8317 {
		t.Fatalf("server.port = %d", got)
	}
	_, err = Resolve(mustSchema(t), Process{Environ: []string{"CLIPROXY_PROFILE=development", "HOME=/home/u"}}, files)
	wantProblem(t, err, "CLIPROXY_PROTECTED_FILE: required; its default needs XDG_RUNTIME_DIR")
}

func TestEmptyEnvironmentVariableIsUnset(t *testing.T) {
	_, err := Resolve(mustSchema(t), Process{Environ: []string{"CLIPROXY_PROFILE=production", "CLIPROXY_PROTECTED_FILE="}}, memFiles{})
	wantProblem(t, err, "CLIPROXY_PROTECTED_FILE: required")
}

func TestInputsNotInTheProfileAreIgnored(t *testing.T) {
	files := memFiles{protectedPath: validProtected(), "/etc/a.yaml": {data: "server: {port: 9000}\noauth: {auth-dir: /var/lib/a}\n"}}
	eff, err := Resolve(mustSchema(t), Process{Environ: production("HOME=relative", "CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("reading HOME in production did not panic")
		}
	}()
	eff.String("HOME")
}

func TestFileLayersKeepTheirClass(t *testing.T) {
	files := memFiles{
		protectedPath: {data: "CLIPROXY_API_KEYS='k'\nCLIPROXY_PROFILE='production'\nCLIPROXY_API_KEYS='k2'\n", mode: 0o600},
		"/etc/a.yaml": {data: "requests: {proxy-url: http://user:pass@proxy}\nserver: {porrt: 1}\nunknown: 1\n"},
	}
	_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	wantProblem(t, err, protectedPath+": CLIPROXY_PROFILE is not a protected input")
	wantProblem(t, err, protectedPath+": CLIPROXY_API_KEYS is set twice")
	wantProblem(t, err, "/etc/a.yaml: requests.proxy-url is a protected input and belongs in the protected file as CLIPROXY_PROXY_URL")
	wantProblem(t, err, "/etc/a.yaml: server.porrt is not an input")
	wantProblem(t, err, "/etc/a.yaml: unknown is not an input")
}

func TestFileValuesMustHaveTheirShape(t *testing.T) {
	files := memFiles{
		protectedPath: {data: "CLIPROXY_API_KEYS='secret-value-1,'\n", mode: 0o600},
		"/etc/a.yaml": {data: "server: {port: \"9000\"}\nrouting: {strategy: random, retry: {request-retry: 11}}\n"},
	}
	_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	wantProblem(t, err, "server.port: want an integer")
	wantProblem(t, err, "routing.strategy: want one of")
	wantProblem(t, err, "routing.retry.request-retry: want at most 10")
	wantProblem(t, err, "access.api-keys: want items of at least 1 characters")
	if strings.Contains(err.Error(), "secret-value-1") {
		t.Fatal("an error printed a protected value")
	}
}

func TestPresentLayersFailClosed(t *testing.T) {
	cases := map[string]memFiles{
		"accessible by its owner alone": {protectedPath: {data: "CLIPROXY_API_KEYS='k'\n", mode: 0o640}},
		"unreadable":                    {protectedPath: {err: fs.ErrPermission}},
		"line 1 is not NAME='value'":    {protectedPath: {data: "CLIPROXY_API_KEYS=k\n", mode: 0o600}},
		"line 2 is not NAME='value'":    {protectedPath: {data: "\naccess: {api-keys: [k]}\n", mode: 0o600}},
		"malformed YAML":                {"/etc/a.yaml": {data: "server: [\n"}},
		"want a mapping of input keys":  {"/etc/a.yaml": {data: "- k\n"}},
	}
	for want, files := range cases {
		_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
		wantProblem(t, err, want)
	}
}

func TestCommandLineMustBeDeclaredFlags(t *testing.T) {
	for args, want := range map[string]string{
		"-config=x.yaml":             "flag provided but not defined: -config",
		"serve":                      `unexpected argument "serve"`,
		"-oauth-callback-port=abc":   "-oauth-callback-port: want an integer",
		"-oauth-callback-port=70000": "-oauth-callback-port: want at most 65535",
	} {
		_, err := Resolve(mustSchema(t), Process{Args: []string{args}, Environ: production()}, memFiles{})
		wantProblem(t, err, want)
	}
}

func TestHelpRequestIsNotAProblem(t *testing.T) {
	_, err := Resolve(mustSchema(t), Process{Args: []string{"-h"}}, memFiles{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	usage := mustSchema(t).Usage("CLIProxyAPI")
	for _, want := range []string{"  -codex-device-login\n", "  CLIPROXY_PROFILE\n"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage lacks %q:\n%s", want, usage)
		}
	}
}

func TestSchemaRejectsMalformedDeclarations(t *testing.T) {
	const head = `{"profiles": ["development", "production"], "inputs": [
	 {"name": "CLIPROXY_PROFILE", "env": "CLIPROXY_PROFILE", "class": "configuration", "description": "p",
	  "shape": {"type": "string", "enum": ["development", "production"]}, "defaults": {}},
	 {"name": "CLIPROXY_OVERLAY_FILES", "env": "CLIPROXY_OVERLAY_FILES", "class": "configuration", "description": "o",
	  "shape": {"type": "path-list"}, "defaults": {}},
	 {"name": "CLIPROXY_PROTECTED_FILE", "env": "CLIPROXY_PROTECTED_FILE", "class": "configuration", "description": "f",
	  "shape": {"type": "path"}, "defaults": {}}`
	if _, err := Parse([]byte(head + `]}`)); err != nil {
		t.Fatalf("minimal schema: %v", err)
	}
	for want, extra := range map[string]string{
		"declared twice":                                `{"name": "CLIPROXY_PROFILE", "env": "X", "class": "configuration", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"must come from the protected file":             `{"name": "S", "env": "S", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"declare its key and secret name":               `{"name": "k", "key": "a.k", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"only protected inputs have a secret":           `{"name": "k", "key": "a.k", "secret": "K", "class": "configuration", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"share the secret name":                         `{"name": "k", "key": "a.k", "secret": "CLIPROXY_PROFILE", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {}}, {"name": "j", "key": "a.j", "secret": "CLIPROXY_PROFILE", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"may default only to empty":                     `{"name": "k", "key": "a.k", "secret": "K", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {"production": "secret"}}`,
		"exactly one of env, flag, and key":             `{"name": "k", "key": "a.k", "flag": "k", "class": "configuration", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"does not apply to":                             `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "string"}, "profiles": ["development"], "defaults": {"production": ""}}`,
		"want an integer":                               `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "integer"}, "defaults": {"production": "1"}}`,
		"not a string or path environment input":        `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "path"}, "defaults": {"production": "${MISSING}/x"}}`,
		"unknown upstream family":                       `{"name": "g", "key": "api-keys.x", "class": "configuration", "description": "d", "shape": {"type": "upstream-groups", "family": "x"}, "defaults": {}}`,
		"must be configuration keyed api-keys.<family>": `{"name": "g", "key": "a.meta", "class": "configuration", "credentials": "k", "description": "d", "shape": {"type": "upstream-groups", "family": "meta"}, "defaults": {}}, ` + credentialInput,
		"must name a protected credential-list input":   `{"name": "g", "key": "api-keys.meta", "class": "configuration", "credentials": "m", "description": "d", "shape": {"type": "upstream-groups", "family": "meta"}, "defaults": {}}, ` + credentialInput,
		"is no upstream-groups input's credentials":     credentialInput,
		"only upstream-groups inputs have credentials":  `{"name": "s", "key": "a.s", "class": "configuration", "credentials": "k", "description": "d", "shape": {"type": "string"}, "defaults": {}}, ` + credentialInput,
		"want an empty list":                            `{"name": "g", "key": "api-keys.meta", "class": "configuration", "credentials": "k", "description": "d", "shape": {"type": "upstream-groups", "family": "meta"}, "defaults": {"production": [{"name": "m"}]}}, ` + credentialInput,
	} {
		_, err := Parse([]byte(head + ",\n" + extra + `]}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

func TestConfigDocumentNestsKeysUnderTheV8Layout(t *testing.T) {
	files := memFiles{protectedPath: validProtected(), "/etc/a.yaml": {data: "server: {port: 9000}\noauth: {auth-dir: /var/lib/a}\n"}}
	eff, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	if err != nil {
		t.Fatal(err)
	}
	doc, errDoc := eff.ConfigDocument()
	if errDoc != nil {
		t.Fatal(errDoc)
	}
	for _, want := range []string{"config-version: 8\n", "server:\n    host: 127.0.0.1\n    port: 9000\n", "access:\n    api-keys:\n        - client-key-1\n"} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("document lacks %q:\n%s", want, doc)
		}
	}
	if strings.Contains(string(doc), "telemetry") {
		t.Fatalf("document carries an entry-point-only key:\n%s", doc)
	}
	if strings.Contains(string(doc), "\napi-keys:") {
		t.Fatalf("document carries empty upstream groups:\n%s", doc)
	}
}

const upstreamOverlay = `api-keys:
  openai-compatibility:
    - name: alpha
      base-url: https://alpha.invalid/v1
      models:
        - {name: model-a, max-context-length: 1000, input-modalities: [text, image], thinking: {levels: [max]}}
    - name: beta
      base-url: https://beta.invalid/v1
      prefix: beta
      models: [{name: org/model-b, alias: model-b}]
  meta:
    - name: muse
      models: [{name: muse-model}]
`

func upstreamFiles(overlay, protected string) memFiles {
	return memFiles{
		protectedPath: {data: "CLIPROXY_API_KEYS='client-key-1'\n" + protected, mode: 0o600},
		"/etc/a.yaml": {data: "server: {port: 9000}\noauth: {auth-dir: /var/lib/a}\n" + overlay},
	}
}

func TestUpstreamGroupsCarryTheirKeys(t *testing.T) {
	files := upstreamFiles(upstreamOverlay, "CLIPROXY_OPENAI_COMPATIBILITY_KEYS='alpha=key-a1,beta=key-b1,gamma=key-g1,alpha=key-a2'\nCLIPROXY_META_KEYS='muse=key-m1'\n")
	eff, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	if err != nil {
		t.Fatal(err)
	}
	doc, errDoc := eff.ConfigDocument()
	if errDoc != nil {
		t.Fatal(errDoc)
	}
	var parsed struct {
		APIKeys map[string][]renderedGroup `yaml:"api-keys"`
	}
	if errParse := yaml.Unmarshal(doc, &parsed); errParse != nil {
		t.Fatal(errParse)
	}
	keys := func(g renderedGroup) []string {
		var out []string
		for _, k := range g.Keys {
			out = append(out, g.Name+"="+k.APIKey)
		}
		return out
	}
	compat, meta := parsed.APIKeys["openai-compatibility"], parsed.APIKeys["meta"]
	if len(parsed.APIKeys) != 2 || len(compat) != 2 || len(meta) != 1 {
		t.Fatalf("api-keys = %+v", parsed.APIKeys)
	}
	if got := append(append(keys(compat[0]), keys(compat[1])...), keys(meta[0])...); !slices.Equal(got, []string{"alpha=key-a1", "alpha=key-a2", "beta=key-b1", "muse=key-m1"}) {
		t.Fatalf("keys = %v", got)
	}
	if m := compat[0].Models[0]; compat[1].Prefix != "beta" || m.MaxContextLength != 1000 || !slices.Equal(m.InputModalities, []string{"text", "image"}) || !slices.Equal(m.Thinking.Levels, []string{"max"}) {
		t.Fatalf("groups = %+v", compat)
	}
	if strings.Contains(string(doc), "compatibility-keys") || strings.Contains(string(doc), "meta-keys") {
		t.Fatalf("document carries a credentials input under its own key:\n%s", doc)
	}
	if strings.Contains(string(doc), "key-g1") {
		t.Fatalf("document carries the key of an undeclared group:\n%s", doc)
	}
	// Keys may arrive before the overlay that declares their groups.
	files = upstreamFiles("", "CLIPROXY_OPENAI_COMPATIBILITY_KEYS='alpha=key-a1'\n")
	if _, err = Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files); err != nil {
		t.Fatalf("keys without groups: %v", err)
	}
}

func TestUpstreamGroupsFailClosed(t *testing.T) {
	cases := []struct {
		overlay, protected, want string
	}{
		{upstreamOverlay, "CLIPROXY_OPENAI_COMPATIBILITY_KEYS='alpha=secret-a'\nCLIPROXY_META_KEYS='muse=secret-m'\n",
			"api-keys.openai-compatibility-keys: group beta has no key"},
		{upstreamOverlay, "CLIPROXY_OPENAI_COMPATIBILITY_KEYS='alpha=secret-a,beta=secret-b'\n",
			"api-keys.meta-keys: group muse has no key"},
		{"", "CLIPROXY_META_KEYS='secret-m'\n", "CLIPROXY_META_KEYS: item 1: want group=key"},
		{"api-keys:\n  meta:\n    - {name: muse, keys: [{api-key: secret-m}]}\n", "",
			"api-keys.meta: want groups of name, base-url, prefix, and models; keys belong in the protected file"},
		{"api-keys:\n  openai-compatibility:\n    - {name: alpha, models: [{name: m}]}\n", "", "api-keys.openai-compatibility: group alpha: base-url is required"},
		{"api-keys:\n  openai-compatibility:\n    - {name: alpha, base-url: https://a.invalid}\n", "", "group alpha: models are required"},
		{"api-keys:\n  openai-compatibility:\n    - {name: alpha, base-url: 'https://u:secret-p@a.invalid', models: [{name: m}]}\n", "",
			"group alpha: base-url must be an http or https URL without credentials"},
		{"api-keys:\n  meta:\n    - {name: muse}\n    - {name: muse}\n", "", "api-keys.meta: group muse is declared twice"},
		{"api-keys:\n  meta:\n    - {name: Muse}\n", "", "api-keys.meta: group 1: want a name of lowercase letters"},
		{"api-keys:\n  meta:\n    - {name: muse, models: [{name: m, input-modalities: [text]}]}\n", "",
			"group muse: model m: the meta family takes no input-modalities"},
		{"api-keys:\n  meta:\n    - {name: muse, models: [{name: m, thinking: {levels: []}}]}\n", "", "group muse: model m: thinking needs non-empty levels"},
		{"api-keys:\n  openai-compatibility-keys: secret-x\n", "",
			"api-keys.openai-compatibility-keys is a protected input and belongs in the protected file as CLIPROXY_OPENAI_COMPATIBILITY_KEYS"},
	}
	for _, c := range cases {
		_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, upstreamFiles(c.overlay, c.protected))
		wantProblem(t, err, c.want)
		if strings.Contains(err.Error(), "secret-") {
			t.Fatalf("an error printed a protected value: %v", err)
		}
	}
}

const credentialInput = `{"name": "k", "key": "a.k", "secret": "K", "class": "protected", "description": "d", "shape": {"type": "credential-list"}, "defaults": {"production": []}}`
