//go:build unit

package main

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
  force-model-prefix: true
  retry: {request-retry: 5, max-retry-interval: 90}
client:
  codex: {enable-apply-patch: true, optimize-multi-agent-v2: true}
api-keys:
  openai-compatibility:
    - name: alpha
      base-url: https://alpha.invalid/v1
      prefix: alpha
      models:
        - {name: org/model-a, alias: model-a, max-context-length: 480000, input-modalities: [text, image], thinking: {levels: [max]}}
  meta:
    - name: muse
      models: [{name: muse-model, max-context-length: 480000}]
  commandcode:
    - name: cc
      prefix: cc
`

const protected = `CLIPROXY_API_KEYS='client-key-a,client-key-b'
CLIPROXY_PROXY_URL='http://proxy.invalid:3128'
OTEL_EXPORTER_OTLP_HEADERS='authorization=Basic%20c2VjcmV0'
CLIPROXY_OPENAI_COMPATIBILITY_KEYS='alpha=upstream-key-a1,alpha=upstream-key-a2'
CLIPROXY_META_KEYS='muse=upstream-key-m'
CLIPROXY_COMMANDCODE_KEYS='cc=upstream-key-c1,cc=upstream-key-c2'
`

func productionProcess(args ...string) inputs.Process {
	return inputs.Process{Args: args, Environ: []string{
		"CLIPROXY_PROFILE=production",
		"CLIPROXY_OVERLAY_FILES=/etc/cliapiproxy/overlay.yaml",
		"CLIPROXY_PROTECTED_FILE=/run/cliapiproxy/protected.env",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
		"CLIPROXY_DEPLOYMENT_ENVIRONMENT=prod",
	}}
}

var testFiles = memFiles{"/etc/cliapiproxy/overlay.yaml": overlay, "/run/cliapiproxy/protected.env": protected}

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
		"routing.force-model-prefix":           cfg.ForceModelPrefix,
		"routing.retry.request-retry":          cfg.RequestRetry,
		"routing.retry.max-retry-interval":     cfg.MaxRetryInterval,
		"client.codex.enable-apply-patch":      cfg.Client.Codex.EnableApplyPatch,
		"client.codex.optimize-multi-agent-v2": cfg.Client.Codex.OptimizeMultiAgentV2,
		"api-keys.openai-compatibility":        openAICompatibilitySummary(cfg.OpenAICompatibility),
		"api-keys.meta":                        metaSummary(cfg.MetaKey),
		"api-keys.commandcode":                 cfg.CommandCodeKey,
	}
	want := map[string]any{
		"server.host":                          "0.0.0.0",
		"server.port":                          9100,
		"oauth.auth-dir":                       "/var/lib/cliapiproxy/auths",
		"access.api-keys":                      []string{"client-key-a", "client-key-b"},
		"requests.proxy-url":                   "http://proxy.invalid:3128",
		"routing.strategy":                     "fill-first",
		"routing.force-model-prefix":           true,
		"routing.retry.request-retry":          5,
		"routing.retry.max-retry-interval":     90,
		"client.codex.enable-apply-patch":      true,
		"client.codex.optimize-multi-agent-v2": true,
		"api-keys.openai-compatibility": []string{
			"alpha https://alpha.invalid/v1 prefix=alpha keys=[upstream-key-a1 upstream-key-a2]",
			"  org/model-a as model-a context=480000 input=[text image] levels=[max]",
		},
		"api-keys.meta": []string{"upstream-key-m prefix= models=[muse-model:480000]"},
		"api-keys.commandcode": []config.CommandCodeKey{
			{APIKey: "upstream-key-c1", Prefix: "cc"}, {APIKey: "upstream-key-c2", Prefix: "cc"},
		},
	}
	schema, errSchema := inputs.Load()
	if errSchema != nil {
		t.Fatal(errSchema)
	}
	for _, in := range schema.Inputs {
		// A credential-list input reaches the configuration inside its groups.
		if in.Key == "" || in.EntryPointOnly || in.Shape.Type == inputs.TypeCredentialList {
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

func openAICompatibilitySummary(groups []config.OpenAICompatibility) []string {
	var out []string
	for _, g := range groups {
		var keys []string
		for _, k := range g.APIKeyEntries {
			keys = append(keys, k.APIKey)
		}
		out = append(out, fmt.Sprintf("%s %s prefix=%s keys=%v", g.Name, g.BaseURL, g.Prefix, keys))
		for _, m := range g.Models {
			var levels []string
			if m.Thinking != nil {
				levels = m.Thinking.Levels
			}
			out = append(out, fmt.Sprintf("  %s as %s context=%d input=%v levels=%v", m.Name, m.Alias, m.MaxContextLength, m.InputModalities, levels))
		}
	}
	return out
}

func metaSummary(keys []config.MetaKey) []string {
	var out []string
	for _, k := range keys {
		var models []string
		for _, m := range k.Models {
			models = append(models, fmt.Sprintf("%s:%d", m.Name, m.MaxContextLength))
		}
		out = append(out, fmt.Sprintf("%s prefix=%s models=%v", k.APIKey, k.Prefix, models))
	}
	return out
}

func TestTelemetryInputsReachTheSettings(t *testing.T) {
	plan, err := resolveRun(productionProcess(), testFiles)
	if err != nil {
		t.Fatal(err)
	}
	s := plan.telemetry
	if s.Traces != "otlp" || s.Metrics != "otlp" || s.Logs != "otlp" {
		t.Fatalf("exporters = %s/%s/%s, want otlp by default", s.Traces, s.Metrics, s.Logs)
	}
	if s.Endpoint != "http://127.0.0.1:4318" || s.Environment != "prod" || s.Headers["authorization"] != "Basic c2VjcmV0" {
		t.Fatalf("settings = %+v", s)
	}
	files := memFiles{"/etc/cliapiproxy/overlay.yaml": overlay, "/run/cliapiproxy/protected.env": strings.Replace(protected, "authorization=Basic%20c2VjcmV0", "novalue", 1)}
	_, err = resolveRun(productionProcess(), files)
	if err == nil || strings.Contains(err.Error(), "novalue") || !strings.Contains(err.Error(), "telemetry.otlp-headers") {
		t.Fatalf("error = %v, want a header problem without the value", err)
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
	plan, err = resolveRun(productionProcess("-telemetry-probe"), testFiles)
	if err != nil || plan.command != nil || !plan.telemetryProbe {
		t.Fatalf("plan = %+v, err = %v; want the telemetry probe", plan, err)
	}
	plan, err = resolveRun(productionProcess("-validate"), testFiles)
	if err != nil || plan.command != nil || !plan.validate {
		t.Fatalf("plan = %+v, err = %v; want only validation", plan, err)
	}
	for args, want := range map[string][]string{
		"choose one command, not -codex-login, -xai-login":       {"-codex-login", "-xai-login"},
		"-vertex-import-prefix applies only with -vertex-import": {"-vertex-import-prefix=p"},
		"choose one command, not -codex-login, -telemetry-probe": {"-codex-login", "-telemetry-probe"},
		"choose one command, not -codex-login, -validate":        {"-codex-login", "-validate"},
	} {
		_, err = resolveRun(productionProcess(want...), testFiles)
		if err == nil || !strings.Contains(err.Error(), args) {
			t.Fatalf("error = %v, want %q", err, args)
		}
	}
}
