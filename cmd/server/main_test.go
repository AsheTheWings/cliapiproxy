//go:build unit

package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AsheTheWings/cliapiproxy/internal/api/handlers/management"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/inputs"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type memFiles map[string]string

func (m memFiles) ReadFile(name string) ([]byte, fs.FileMode, error) {
	data, ok := m[name]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	return []byte(data), 0o600, nil
}

// sharedFiles serves one of its files readable by everyone.
type sharedFiles struct {
	files  memFiles
	shared string
}

func (m sharedFiles) ReadFile(name string) ([]byte, fs.FileMode, error) {
	data, mode, err := m.files.ReadFile(name)
	if err == nil && name == m.shared {
		mode = 0o644
	}
	return data, mode, err
}

const overlay = `server: {host: 0.0.0.0, port: 9100}
oauth:
  auth-dir: /var/lib/cliapiproxy/auths
  models:
    codex: [gpt-6-sol]
    kimi-ai: [Kimi-K3]
routing:
  strategy: fill-first
  force-model-prefix: true
  retry: {request-retry: 5, max-retry-interval: 90}
client:
  codex: {enable-apply-patch: true, optimize-multi-agent-v2: true}
management: {allow-remote: true}
keys: {state-file: /var/lib/cliapiproxy/keys.yaml}
`

// keyState holds every key the server uses: the client keys and each
// family's upstream groups.
const keyState = `access:
  api-keys: [client-key-a, client-key-b]
api-keys:
  openai-compatibility:
    - name: alpha
      base-url: https://alpha.invalid/v1
      prefix: alpha
      models:
        - {name: org/model-a, alias: model-a, max-context-length: 480000, input-modalities: [text, image], thinking: {levels: [max]}}
      keys: [{api-key: upstream-key-a1}, {api-key: upstream-key-a2}]
  meta:
    - name: muse
      models: [{name: muse-model, max-context-length: 480000}]
      keys: [{api-key: upstream-key-m}]
  commandcode:
    - name: cc
      prefix: cc
      models: [{name: commandcode/deepseek/deepseek-v4.1-flash, alias: flash}]
      keys: [{api-key: upstream-key-c1}, {api-key: upstream-key-c2}]
  kiro:
    - name: kiro
      keys: [{api-key: upstream-key-k}]
`

const protected = `CLIPROXY_PROXY_URL='http://proxy.invalid:3128'
CLIPROXY_MANAGEMENT_KEY='management-key'
OTEL_EXPORTER_OTLP_HEADERS='authorization=Basic%20c2VjcmV0'
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

const keyStatePath = "/var/lib/cliapiproxy/keys.yaml"

var testFiles = memFiles{"/etc/cliapiproxy/overlay.yaml": overlay, "/run/cliapiproxy/protected.env": protected, keyStatePath: keyState}

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
		"requests.proxy-url":                   cfg.ProxyURL,
		"management.secret-key":                bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte("management-key")) == nil,
		"management.allow-remote":              cfg.RemoteManagement.AllowRemote,
		"routing.strategy":                     cfg.Routing.Strategy,
		"routing.force-model-prefix":           cfg.ForceModelPrefix,
		"routing.retry.request-retry":          cfg.RequestRetry,
		"routing.retry.max-retry-interval":     cfg.MaxRetryInterval,
		"client.codex.enable-apply-patch":      cfg.Client.Codex.EnableApplyPatch,
		"client.codex.optimize-multi-agent-v2": cfg.Client.Codex.OptimizeMultiAgentV2,
		"oauth.models.antigravity":             cfg.OAuthModels["antigravity"],
		"oauth.models.claude":                  cfg.OAuthModels["claude"],
		"oauth.models.codex":                   cfg.OAuthModels["codex"],
		"oauth.models.devin":                   cfg.OAuthModels["devin"],
		"oauth.models.kimi":                    cfg.OAuthModels["kimi"],
		"oauth.models.kimi-ai":                 cfg.OAuthModels["kimi-ai"],
		"oauth.models.meta":                    cfg.OAuthModels["meta"],
		"oauth.models.xai":                     cfg.OAuthModels["xai"],
	}
	want := map[string]any{
		"server.host":        "0.0.0.0",
		"server.port":        9100,
		"oauth.auth-dir":     "/var/lib/cliapiproxy/auths",
		"requests.proxy-url": "http://proxy.invalid:3128",
		// The server keeps only the key's bcrypt hash.
		"management.secret-key":                true,
		"management.allow-remote":              true,
		"routing.strategy":                     "fill-first",
		"routing.force-model-prefix":           true,
		"routing.retry.request-retry":          5,
		"routing.retry.max-retry-interval":     90,
		"client.codex.enable-apply-patch":      true,
		"client.codex.optimize-multi-agent-v2": true,
		// A provider without a list keeps no entry; names are lowercased.
		"oauth.models.antigravity": []string(nil),
		"oauth.models.claude":      []string(nil),
		"oauth.models.codex":       []string{"gpt-6-sol"},
		"oauth.models.devin":       []string(nil),
		"oauth.models.kimi":        []string(nil),
		"oauth.models.kimi-ai":     []string{"kimi-k3"},
		"oauth.models.meta":        []string(nil),
		"oauth.models.xai":         []string(nil),
	}
	schema, errSchema := inputs.Load()
	if errSchema != nil {
		t.Fatal(errSchema)
	}
	for _, in := range schema.Inputs {
		if in.Key == "" || in.EntryPointOnly {
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
}

// Without a management key the management API stays off.
func TestManagementIsOffWithoutItsKey(t *testing.T) {
	files := memFiles{"/etc/cliapiproxy/overlay.yaml": "server: {port: 9100}\noauth: {auth-dir: /var/lib/cliapiproxy/auths}\nkeys: {state-file: /var/lib/cliapiproxy/keys.yaml}\n"}
	plan, err := resolveRun(productionProcess(), files)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.cfg.RemoteManagement; got.SecretKey != "" || got.AllowRemote {
		t.Fatalf("management = %+v, want no key and loopback only", got)
	}
}

// The key state file is the only source of the client keys and the upstream
// groups, and a state file others can read is refused, since it holds keys.
func TestKeyStateHoldsEveryKey(t *testing.T) {
	plan, err := resolveRun(productionProcess(), testFiles)
	if err != nil {
		t.Fatal(err)
	}
	cfg := plan.cfg
	got := map[string]any{
		"access.api-keys":               cfg.APIKeys,
		"api-keys.openai-compatibility": openAICompatibilitySummary(cfg.OpenAICompatibility),
		"api-keys.meta":                 metaSummary(cfg.MetaKey),
		"api-keys.commandcode":          cfg.CommandCodeKey,
		"api-keys.kiro":                 cfg.KiroKey,
	}
	want := map[string]any{
		"access.api-keys": []string{"client-key-a", "client-key-b"},
		"api-keys.openai-compatibility": []string{
			"alpha https://alpha.invalid/v1 prefix=alpha keys=[upstream-key-a1 upstream-key-a2]",
			"  org/model-a as model-a context=480000 input=[text image] levels=[max]",
		},
		"api-keys.meta": []string{"upstream-key-m prefix= models=[muse-model:480000]"},
		"api-keys.commandcode": []config.CommandCodeKey{
			{APIKey: "upstream-key-c1", Prefix: "cc", Models: []config.CodexModel{{Name: "commandcode/deepseek/deepseek-v4.1-flash", Alias: "flash"}}},
			{APIKey: "upstream-key-c2", Prefix: "cc", Models: []config.CodexModel{{Name: "commandcode/deepseek/deepseek-v4.1-flash", Alias: "flash"}}},
		},
		"api-keys.kiro": []config.KiroKey{{APIKey: "upstream-key-k"}},
	}
	for key, value := range want {
		if !reflect.DeepEqual(got[key], value) {
			t.Errorf("%s = %#v, want %#v", key, got[key], value)
		}
	}
	shared := sharedFiles{files: testFiles, shared: keyStatePath}
	if _, err = resolveRun(productionProcess(), shared); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("err = %v, want a refused state file", err)
	}
	for _, line := range []string{"CLIPROXY_API_KEYS='client-key-a'\n", "CLIPROXY_KIRO_KEYS='kiro=upstream-key-k'\n"} {
		files := memFiles{keyStatePath: keyState, "/etc/cliapiproxy/overlay.yaml": overlay, "/run/cliapiproxy/protected.env": protected + line}
		if _, err = resolveRun(productionProcess(), files); err == nil || !strings.Contains(err.Error(), "is not a protected input") {
			t.Errorf("protected line %q: err = %v, want it refused", line, err)
		}
	}
	files := memFiles{keyStatePath: keyState, "/etc/cliapiproxy/overlay.yaml": overlay + "api-keys:\n  kiro: [{name: k}]\n", "/run/cliapiproxy/protected.env": protected}
	if _, err = resolveRun(productionProcess(), files); err == nil || !strings.Contains(err.Error(), "api-keys is not an input") {
		t.Errorf("overlay api-keys: err = %v, want it refused", err)
	}
}

// The management API's view of the configuration holds no value the
// protected file delivers.
func TestManagementConfigViewRedactsProtectedValues(t *testing.T) {
	plan, err := resolveRun(productionProcess(), testFiles)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	management.NewHandler(plan.cfg, "", nil).ConfigV8Effective(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://alpha.invalid/v1") {
		t.Fatalf("the view lacks the upstream groups: %s", body)
	}
	for _, value := range protectedValues(protected) {
		if strings.Contains(body, value) {
			t.Errorf("the view exposes %q", value)
		}
	}
}

// protectedValues lists every value of a protected file and every key of
// the key state.
func protectedValues(file string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(file), "\n") {
		_, quoted, _ := strings.Cut(line, "=")
		out = append(out, strings.Trim(quoted, "'"))
	}
	for _, key := range []string{"client-key-a", "client-key-b", "upstream-key-a1", "upstream-key-a2", "upstream-key-m", "upstream-key-c1", "upstream-key-c2", "upstream-key-k"} {
		out = append(out, key)
	}
	return out
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
