//go:build component

package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/telemetry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/testsupport/telemetrytest"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestExecutionsWithoutCredentialsTraceAsNoCredential(t *testing.T) {
	rec := telemetrytest.Install(t)
	m := NewManager(nil, nil, nil)
	req := cliproxyexecutor.Request{Model: "telemetry-model"}
	if _, err := m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("Execute without credentials succeeded")
	}
	if _, err := m.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("ExecuteStream without credentials succeeded")
	}
	spans := rec.Spans("cliapiproxy.execute")
	if len(spans) != 2 {
		t.Fatalf("%d execution spans, want 2", len(spans))
	}
	for i, operation := range []string{"execute", "stream"} {
		if v, _ := telemetrytest.Attr(spans[i], telemetry.AttrOperation); v.AsString() != operation {
			t.Fatalf("operation = %q, want %q", v.AsString(), operation)
		}
		if v, _ := telemetrytest.Attr(spans[i], telemetry.AttrErrorType); v.AsString() != telemetry.ErrorNoCredential {
			t.Fatalf("%s error.type = %q, want no_credential", operation, v.AsString())
		}
	}
	rec.CheckCatalog(t)
}
