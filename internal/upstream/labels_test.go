//go:build unit

package upstream

import (
	"strings"
	"testing"
)

func TestNormalizeLabelTrimsAndChecksTheLabel(t *testing.T) {
	for entered, want := range map[string]string{
		"team":                              "team",
		"  Alice's laptop\t":                "Alice's laptop",
		strings.Repeat("é", MaxLabelLength): strings.Repeat("é", MaxLabelLength),
	} {
		if got, err := NormalizeLabel(entered); err != nil || got != want {
			t.Errorf("NormalizeLabel(%q) = %q, %v; want %q", entered, got, err, want)
		}
	}
	for name, entered := range map[string]string{
		"empty":               "",
		"only spaces":         " \t ",
		"too long":            strings.Repeat("n", MaxLabelLength+1),
		"newline":             "a\nb",
		"carriage return":     "a\rb",
		"tab inside":          "a\tb",
		"line separator":      "a\u2028b",
		"paragraph separator": "a\u2029b",
		"invalid UTF-8":       "a\xffb",
	} {
		if got, err := NormalizeLabel(entered); err == nil {
			t.Errorf("%s: NormalizeLabel(%q) = %q, want an error", name, entered, got)
		}
	}
}

func TestValidateLabelAcceptsOnlyStoredForm(t *testing.T) {
	if err := ValidateLabel(""); err != nil {
		t.Errorf("no label: %v", err)
	}
	for _, label := range []string{" padded", "padded ", strings.Repeat("n", MaxLabelLength+1), "two\nlines"} {
		if err := ValidateLabel(label); err == nil {
			t.Errorf("ValidateLabel(%q) accepted", label)
		}
	}
}

func TestValidateKeyLabelsRejectsRepeatsWithoutNamingKeys(t *testing.T) {
	position := func(i int) string { return "key " + string(rune('1'+i)) }
	if err := ValidateKeyLabels([]Key{{APIKey: "secret-a", Label: "x"}, {APIKey: "secret-b"}, {APIKey: "secret-c"}, {APIKey: "secret-d", Label: "y"}}, position); err != nil {
		t.Fatalf("distinct labels and unlabeled keys: %v", err)
	}
	err := ValidateKeyLabels([]Key{{APIKey: "secret-a", Label: "x"}, {APIKey: "secret-b", Label: "x"}}, position)
	if err == nil || !strings.Contains(err.Error(), "key 2") || !strings.Contains(err.Error(), "key 1") || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("repeated label: %v; want both positions and no key", err)
	}
}

func TestFingerprintDependsOnTheKeyAlone(t *testing.T) {
	key := "sk-synthetic-0123456789"
	fingerprint := Fingerprint(key)
	if len(fingerprint) != 12 || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		t.Fatalf("Fingerprint = %q, want 12 lowercase hexadecimal characters", fingerprint)
	}
	// SHA-256("abc") starts with ba7816bf8f01.
	if got := Fingerprint("abc"); got != "ba7816bf8f01" {
		t.Fatalf("Fingerprint(abc) = %q, want ba7816bf8f01", got)
	}
	labeled := Group{Spec: Spec{Name: "alpha", Prefix: "a"}, Keys: []Key{{APIKey: key, Label: "team"}}}
	moved := Group{Spec: Spec{Name: "beta", Prefix: "b"}, Keys: []Key{{APIKey: "other"}, {APIKey: key}}}
	if Fingerprint(labeled.Keys[0].APIKey) != fingerprint || Fingerprint(moved.Keys[1].APIKey) != fingerprint {
		t.Fatal("a label, group, prefix, or position change changed the fingerprint")
	}
	if Fingerprint("sk-synthetic-0123456788") == fingerprint {
		t.Fatal("two keys share a fingerprint")
	}
}

func TestAccountLabelsNameUnlabeledKeys(t *testing.T) {
	key := Key{APIKey: "abc"}
	if got := AccountLabel("alpha", key); got != "alpha ba7816bf8f01" {
		t.Errorf("unlabeled upstream key = %q", got)
	}
	key.Label = "team"
	if AccountLabel("alpha", key) != "team" {
		t.Error("a label does not name its key")
	}
}
