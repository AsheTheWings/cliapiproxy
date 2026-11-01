//go:build unit

package synthesizer

import (
	"fmt"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
)

// synthesizeFamily parses a v8 document holding one group of family whose
// single key carries label, and synthesizes its credentials.
func synthesizeFamily(t *testing.T, family, label string) *coreauth.Auth {
	t.Helper()
	group := "name: team-group, keys: [{api-key: sk-synthetic-key%s}]"
	if family == "openai-compatibility" {
		group = "name: team-group, base-url: 'https://compat.invalid/v1', models: [{name: m}], keys: [{api-key: sk-synthetic-key%s}]"
	}
	labelField := ""
	if label != "" {
		labelField = fmt.Sprintf(", label: %q", label)
	}
	document := fmt.Sprintf("config-version: 8\napi-keys:\n  %s: [{"+group+"}]\n", family, labelField)
	cfg, err := config.ParseConfigBytes([]byte(document))
	if err != nil {
		t.Fatalf("%s: parse: %v", family, err)
	}
	auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: cfg, Now: time.Unix(0, 0), IDGenerator: NewStableIDGenerator()})
	if err != nil || len(auths) != 1 {
		t.Fatalf("%s: synthesized %d credentials, %v", family, len(auths), err)
	}
	return auths[0]
}

func TestKeyLabelsLeaveCredentialIDsUnchanged(t *testing.T) {
	for _, family := range []string{"openai-compatibility", "meta", "commandcode", "kiro"} {
		unlabeled := synthesizeFamily(t, family, "")
		for _, label := range []string{"Alice's account", "renamed account"} {
			labeled := synthesizeFamily(t, family, label)
			if labeled.ID != unlabeled.ID {
				t.Errorf("%s: label %q changed the credential ID from %s to %s", family, label, unlabeled.ID, labeled.ID)
			}
			if got := labeled.Attributes[coreauth.AttributeKeyLabel]; got != label {
				t.Errorf("%s: key label attribute = %q, want %q", family, got, label)
			}
			if got := labeled.Attributes[coreauth.AttributeAccountLabel]; got != label {
				t.Errorf("%s: a labeled key's account label = %q, want %q", family, got, label)
			}
		}
		if _, has := unlabeled.Attributes[coreauth.AttributeKeyLabel]; has {
			t.Errorf("%s: an unlabeled key carries a label attribute", family)
		}
		want := "team-group " + upstream.Fingerprint("sk-synthetic-key")
		if got := unlabeled.Attributes[coreauth.AttributeAccountLabel]; got != want {
			t.Errorf("%s: an unlabeled key's account label = %q, want %q", family, got, want)
		}
	}
}

func TestKeyCredentialsKeepTheirGroupName(t *testing.T) {
	for _, family := range []string{"openai-compatibility", "meta", "commandcode", "kiro"} {
		if got := synthesizeFamily(t, family, "").Attributes[coreauth.AttributeGroup]; got != "team-group" {
			t.Errorf("%s: group attribute = %q, want team-group", family, got)
		}
	}
}
