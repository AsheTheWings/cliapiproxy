//go:build unit

package inputs

import (
	"errors"
	"flag"
	"io/fs"
	"slices"
	"strings"
	"testing"
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

const protectedPath = "/run/cliapiproxy/protected.yaml"

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
	return memFile{data: "access:\n  api-keys: [client-key-1]\n", mode: 0o600}
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
	files := memFiles{"/run/user/1/cliapiproxy/protected.yaml": validProtected()}
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
		protectedPath: {data: "access: {api-keys: [k]}\nserver: {port: 9000}\n", mode: 0o600},
		"/etc/a.yaml": {data: "requests: {proxy-url: http://user:pass@proxy}\nserver: {porrt: 1}\nunknown: 1\n"},
	}
	_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	wantProblem(t, err, protectedPath+": server.port is a configuration input and belongs in the overlay files")
	wantProblem(t, err, "/etc/a.yaml: requests.proxy-url is a protected input and belongs in the protected file")
	wantProblem(t, err, "/etc/a.yaml: server.porrt is not an input")
	wantProblem(t, err, "/etc/a.yaml: unknown is not an input")
}

func TestFileValuesMustHaveTheirShape(t *testing.T) {
	files := memFiles{
		protectedPath: {data: "access: {api-keys: secret-value-1}\n", mode: 0o600},
		"/etc/a.yaml": {data: "server: {port: \"9000\"}\nrouting: {strategy: random, retry: {request-retry: 11}}\n"},
	}
	_, err := Resolve(mustSchema(t), Process{Environ: production("CLIPROXY_OVERLAY_FILES=/etc/a.yaml")}, files)
	wantProblem(t, err, "server.port: want an integer")
	wantProblem(t, err, "routing.strategy: want one of")
	wantProblem(t, err, "routing.retry.request-retry: want at most 10")
	wantProblem(t, err, "access.api-keys: want a list")
	if strings.Contains(err.Error(), "secret-value-1") {
		t.Fatal("an error printed a protected value")
	}
}

func TestPresentLayersFailClosed(t *testing.T) {
	cases := map[string]memFiles{
		"accessible by its owner alone": {protectedPath: {data: "access: {api-keys: [k]}\n", mode: 0o640}},
		"unreadable":                    {protectedPath: {err: fs.ErrPermission}},
		"malformed YAML":                {protectedPath: {data: "access: [\n", mode: 0o600}},
		"want a mapping of input keys":  {protectedPath: {data: "- k\n", mode: 0o600}},
	}
	for want, files := range cases {
		_, err := Resolve(mustSchema(t), Process{Environ: production()}, files)
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
		"declared twice":                         `{"name": "CLIPROXY_PROFILE", "env": "X", "class": "configuration", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"must come from the protected file":      `{"name": "S", "env": "S", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"may default only to empty":              `{"name": "k", "key": "a.k", "class": "protected", "description": "d", "shape": {"type": "string"}, "defaults": {"production": "secret"}}`,
		"exactly one of env, flag, and key":      `{"name": "k", "key": "a.k", "flag": "k", "class": "configuration", "description": "d", "shape": {"type": "string"}, "defaults": {}}`,
		"does not apply to":                      `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "string"}, "profiles": ["development"], "defaults": {"production": ""}}`,
		"want an integer":                        `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "integer"}, "defaults": {"production": "1"}}`,
		"not a string or path environment input": `{"name": "k", "key": "a.k", "class": "configuration", "description": "d", "shape": {"type": "path"}, "defaults": {"production": "${MISSING}/x"}}`,
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
}
