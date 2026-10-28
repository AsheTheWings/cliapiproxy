//go:build unit

package main

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/inputs"
)

type memFiles map[string]string

func (m memFiles) ReadFile(name string) ([]byte, fs.FileMode, error) {
	data, ok := m[name]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	return []byte(data), 0o600, nil
}

const overlay = `server: {host: 0.0.0.0, port: 9100}
oauth: {auth-dir: /var/lib/cliapiproxy/auths}
routing:
  strategy: fill-first
  retry: {request-retry: 5, max-retry-interval: 90}
client:
  codex: {enable-apply-patch: true, optimize-multi-agent-v2: true}
observability:
  logs: {debug: true}
`

const protected = `access: {api-keys: [client-key-a, client-key-b]}
requests: {proxy-url: "http://proxy.invalid:3128"}
`

func productionProcess(args ...string) inputs.Process {
	return inputs.Process{Args: args, Environ: []string{
		"CLIPROXY_PROFILE=production",
		"CLIPROXY_OVERLAY_FILES=/etc/cliapiproxy/overlay.yaml",
		"CLIPROXY_PROTECTED_FILE=/run/cliapiproxy/protected.yaml",
	}}
}

var testFiles = memFiles{"/etc/cliapiproxy/overlay.yaml": overlay, "/run/cliapiproxy/protected.yaml": protected}

// Every keyed input must reach the configuration the server runs with.
func TestEveryKeyedInputReachesTheConfiguration(t *testing.T) {
	plan, err := resolveRun(productionProcess(), testFiles)
	if err != nil {
		t.Fatal(err)
	}
	cfg := plan.cfg
	got := map[string]any{
		"server.host":                          cfg.Host,
		"server.port":                          cfg.Port,
		"oauth.auth-dir":                       cfg.AuthDir,
		"access.api-keys":                      cfg.APIKeys,
		"requests.proxy-url":                   cfg.ProxyURL,
		"routing.strategy":                     cfg.Routing.Strategy,
		"routing.retry.request-retry":          cfg.RequestRetry,
		"routing.retry.max-retry-interval":     cfg.MaxRetryInterval,
		"client.codex.enable-apply-patch":      cfg.Client.Codex.EnableApplyPatch,
		"client.codex.optimize-multi-agent-v2": cfg.Client.Codex.OptimizeMultiAgentV2,
		"observability.logs.debug":             cfg.Debug,
	}
	want := map[string]any{
		"server.host":                          "0.0.0.0",
		"server.port":                          9100,
		"oauth.auth-dir":                       "/var/lib/cliapiproxy/auths",
		"access.api-keys":                      []string{"client-key-a", "client-key-b"},
		"requests.proxy-url":                   "http://proxy.invalid:3128",
		"routing.strategy":                     "fill-first",
		"routing.retry.request-retry":          5,
		"routing.retry.max-retry-interval":     90,
		"client.codex.enable-apply-patch":      true,
		"client.codex.optimize-multi-agent-v2": true,
		"observability.logs.debug":             true,
	}
	schema, errSchema := inputs.Load()
	if errSchema != nil {
		t.Fatal(errSchema)
	}
	for _, in := range schema.Inputs {
		if in.Key == "" {
			continue
		}
		if _, covered := want[in.Key]; !covered {
			t.Errorf("%s has no expectation here; add one", in.Key)
			continue
		}
		if !reflect.DeepEqual(got[in.Key], want[in.Key]) {
			t.Errorf("%s = %#v, want %#v", in.Key, got[in.Key], want[in.Key])
		}
	}
	if cfg.RemoteManagement.SecretKey != "" {
		t.Fatal("the management API must stay disabled")
	}
}

func TestCommandsAreExclusive(t *testing.T) {
	plan, err := resolveRun(productionProcess(), testFiles)
	if err != nil || plan.command != nil {
		t.Fatalf("plan = %+v, err = %v; want to serve", plan, err)
	}
	plan, err = resolveRun(productionProcess("-codex-device-login", "-no-browser"), testFiles)
	if err != nil || plan.command == nil {
		t.Fatalf("err = %v; want the login command", err)
	}
	for args, want := range map[string][]string{
		"choose one command, not -codex-login, -xai-login":       {"-codex-login", "-xai-login"},
		"-vertex-import-prefix applies only with -vertex-import": {"-vertex-import-prefix=p"},
	} {
		_, err = resolveRun(productionProcess(want...), testFiles)
		if err == nil || !strings.Contains(err.Error(), args) {
			t.Fatalf("error = %v, want %q", err, args)
		}
	}
}
