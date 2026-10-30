//go:build qualification

// Package qualification runs the built container image, from the OCI layout
// CI publishes, in a run-owned Podman store.
package qualification

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
)

const (
	// layoutTag is the tag scripts/image.sh gives the image in its layout.
	layoutTag = "qualified"
	// runPrefix starts the name of every container a run creates.
	runPrefix = "cliapiproxy-qualification-"
	clientKey = "qualification-client-key"
	// managementKey turns the management API on, which the dashboard follows.
	managementKey = "qualification-management-key"
	// collectorAuthorization is the header the server must send its collector.
	collectorAuthorization = "Basic cXVhbGlmaWNhdGlvbjpzZWNyZXQ="
	readyTimeout           = 60 * time.Second
	commandTimeout         = 2 * time.Minute
)

// podman runs Podman against a store and runtime directory the run owns.
type podman struct {
	t    *testing.T
	root string
}

func (p podman) command(ctx context.Context, args ...string) *exec.Cmd {
	base := []string{
		"--root", filepath.Join(p.root, "storage"), "--runroot", filepath.Join(p.root, "run"),
		"--tmpdir", filepath.Join(p.root, "tmp"), "--storage-driver", "vfs",
		"--cgroup-manager", "cgroupfs", "--events-backend", "file",
	}
	return exec.CommandContext(ctx, "podman", append(base, args...)...)
}

func (p podman) run(args ...string) string {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := p.command(ctx, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if errRun := cmd.Run(); errRun != nil {
		p.t.Fatalf("podman %s: %v\n%s", args[0], errRun, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// collector is a run-owned OTLP/HTTP endpoint recording what it receives.
type collector struct {
	mu       sync.Mutex
	requests map[string][][]byte
	refused  int
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Header.Get("Authorization") != collectorAuthorization {
		c.refused++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	c.requests[r.URL.Path] = append(c.requests[r.URL.Path], body)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func (c *collector) received(path string, fragment string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.requests[path] {
		if bytes.Contains(body, []byte(fragment)) {
			return true
		}
	}
	return false
}

func (c *collector) refusals() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

func input(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s names no value; run scripts/test.sh qualification", name)
	}
	return value
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if errWrite := os.WriteFile(path, []byte(content), mode); errWrite != nil {
		t.Fatal(errWrite)
	}
}

// manifestAnnotations reads the annotations of the layout's only image.
func manifestAnnotations(t *testing.T, layout string) map[string]string {
	t.Helper()
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	raw, errIndex := os.ReadFile(filepath.Join(layout, "index.json"))
	if errIndex != nil || json.Unmarshal(raw, &index) != nil || len(index.Manifests) != 1 {
		t.Fatalf("the layout must index one image: %v", errIndex)
	}
	var manifest struct {
		Annotations map[string]string `json:"annotations"`
	}
	raw, errManifest := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")))
	if errManifest != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatalf("read the image manifest: %v", errManifest)
	}
	return manifest.Annotations
}

func TestImageServesAuthenticatedRequestsAndExportsTelemetry(t *testing.T) {
	layout := input(t, "CLIPROXY_QUALIFICATION_LAYOUT")
	revision := input(t, "CLIPROXY_QUALIFICATION_REVISION")
	annotations := manifestAnnotations(t, layout)
	for key, want := range map[string]string{
		"org.opencontainers.image.revision": revision,
		"org.opencontainers.image.version":  "0.0.0",
	} {
		if annotations[key] != want {
			t.Fatalf("the image annotates %s=%q, want %q", key, annotations[key], want)
		}
	}
	if annotations["org.opencontainers.image.source"] == "" {
		t.Fatal("the image names no source")
	}

	runID := make([]byte, 6)
	if _, errRand := rand.Read(runID); errRand != nil {
		t.Fatal(errRand)
	}
	name := runPrefix + hex.EncodeToString(runID)
	probe := name + "-probe"
	// Podman refuses a runroot path longer than 50 characters, so the run's
	// directory has a short name; the containers carry the run's name.
	root, errRoot := os.MkdirTemp("", "q-")
	if errRoot != nil {
		t.Fatal(errRoot)
	}
	if runroot := filepath.Join(root, "run"); len(runroot) > 50 {
		t.Fatalf("the Podman runroot %s is longer than 50 characters; run with a shorter TMPDIR", runroot)
	}
	p := podman{t: t, root: root}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		// The store holds files of mapped IDs, so Podman removes it first.
		_ = p.command(ctx, "rm", "--force", "--time", "0", name, probe).Run()
		_ = p.command(ctx, "system", "reset", "--force").Run()
		if errRemove := os.RemoveAll(root); errRemove != nil {
			t.Errorf("remove the run directory %s: %v", root, errRemove)
		}
	})
	for _, dir := range []string{"tmp", "inputs", "auths", "keys"} {
		if errMkdir := os.MkdirAll(filepath.Join(root, dir), 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
	}
	image := p.run("pull", "--quiet", "oci:"+layout+":"+layoutTag)

	sink := &collector{requests: map[string][][]byte{}}
	otlp := httptest.NewServer(sink)
	t.Cleanup(otlp.Close)

	port := freePort(t)
	inputs := filepath.Join(root, "inputs")
	writeFile(t, filepath.Join(inputs, "overlay.yaml"), fmt.Sprintf(
		"server: {port: %d}\noauth: {auth-dir: /data/auths}\nkeys: {state-file: /data/keys/keys.yaml}\n", port), 0o600)
	writeFile(t, filepath.Join(inputs, "protected.env"), fmt.Sprintf(
		"CLIPROXY_MANAGEMENT_KEY='%s'\nOTEL_EXPORTER_OTLP_HEADERS='Authorization=%s'\n",
		managementKey, strings.ReplaceAll(collectorAuthorization, " ", "%20")), 0o600)
	// The server keeps its client keys as state.
	writeFile(t, filepath.Join(root, "keys", "keys.yaml"), fmt.Sprintf("access: {api-keys: [%s]}\n", clientKey), 0o600)
	containerArgs := func(extra ...string) []string {
		args := []string{
			"--network", "host", "--userns", "keep-id:uid=65532,gid=65532", "--read-only",
			"--volume", inputs + ":/etc/cliapiproxy:ro", "--volume", filepath.Join(root, "auths") + ":/data/auths",
			"--volume", filepath.Join(root, "keys") + ":/data/keys",
			"--env", "CLIPROXY_PROFILE=production",
			"--env", "CLIPROXY_OVERLAY_FILES=/etc/cliapiproxy/overlay.yaml",
			"--env", "CLIPROXY_PROTECTED_FILE=/etc/cliapiproxy/protected.env",
			"--env", "OTEL_EXPORTER_OTLP_ENDPOINT=" + otlp.URL,
			"--env", "CLIPROXY_DEPLOYMENT_ENVIRONMENT=qualification",
		}
		// Arguments after the image reach the server.
		return append(append(args, image), extra...)
	}

	if output := p.run(append([]string{"run", "--rm"}, containerArgs("-validate")...)...); output != "valid" {
		t.Fatalf("-validate printed %q, want valid", output)
	}
	p.run(append([]string{"run", "--detach", "--name", name}, containerArgs()...)...)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path, key string) int {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	wait.Until(t, readyTimeout, func() bool { return get("/healthz", "") == http.StatusOK }, "the server never became ready on port %d", port)
	if status := get("/v1/models", ""); status != http.StatusUnauthorized {
		t.Fatalf("GET /v1/models without a key = %d, want 401", status)
	}
	if status := get("/v1/models", clientKey); status != http.StatusOK {
		t.Fatalf("GET /v1/models with the client key = %d, want 200", status)
	}
	if status := get("/management.html", ""); status != http.StatusOK {
		t.Fatalf("GET /management.html = %d, want 200 from the embedded dashboard", status)
	}

	p.run("stop", "--time", "30", name)
	if code := p.run("inspect", "--format", "{{.State.ExitCode}}", name); code != "0" {
		t.Fatalf("the server exited with %s after SIGTERM, want 0", code)
	}
	version := "0.0.0+" + revision
	for _, path := range []string{"/v1/traces", "/v1/metrics"} {
		if !sink.received(path, version) {
			t.Errorf("the collector received no %s export from version %s", path, version)
		}
	}
	if !sink.received("/v1/traces", "GET /v1/models") {
		t.Error("the collector received no server span of the requests")
	}

	p.run(append([]string{"run", "--detach", "--name", probe}, containerArgs("-telemetry-probe")...)...)
	if code := p.run("wait", probe); code != "0" {
		t.Fatalf("the probe exited with %s, want 0", code)
	}
	output := p.run("logs", probe)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(output) {
		t.Fatalf("the probe printed %q, want one correlation identifier", output)
	}
	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if !sink.received(path, "cliapiproxy.qualification") {
			t.Errorf("the collector received no qualification record on %s", path)
		}
	}
	if !sink.received("/v1/logs", output) || !sink.received("/v1/traces", output) {
		t.Error("the probe's span and log record do not carry its correlation identifier")
	}
	if refused := sink.refusals(); refused != 0 {
		t.Errorf("the collector refused %d unauthenticated exports", refused)
	}
}
