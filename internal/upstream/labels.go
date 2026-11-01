package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLabelLength bounds a key's label, in characters.
const MaxLabelLength = 64

// fingerprintLength is the number of hexadecimal characters of a key
// fingerprint.
const fingerprintLength = 12

// Fingerprint identifies an API key without revealing it: the first 12
// hexadecimal characters of the SHA-256 of the key value alone, so a label,
// group, prefix, or position change leaves it unchanged.
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:fingerprintLength]
}

// NormalizeLabel trims a label as an operator entered it and checks the
// result. An entered label that trims to nothing is rejected.
func NormalizeLabel(entered string) (string, error) {
	label := strings.TrimSpace(entered)
	if label == "" {
		return "", errors.New("label is empty")
	}
	return label, ValidateLabel(label)
}

// ValidateLabel checks a stored label: empty means no label; otherwise it is
// one line of at most MaxLabelLength characters without surrounding spaces
// or control characters.
func ValidateLabel(label string) error {
	if label == "" {
		return nil
	}
	if !utf8.ValidString(label) {
		return errors.New("label is not valid UTF-8")
	}
	if label != strings.TrimSpace(label) {
		return errors.New("label has surrounding spaces")
	}
	if utf8.RuneCountInString(label) > MaxLabelLength {
		return fmt.Errorf("label is longer than %d characters", MaxLabelLength)
	}
	if strings.IndexFunc(label, breaksLine) >= 0 {
		return errors.New("label holds a line break or control character")
	}
	return nil
}

// breaksLine reports control characters and the Unicode line and paragraph
// separators.
func breaksLine(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// AccountLabel names a group's key: its label, or for an unlabeled
// key the group name followed by the key's fingerprint.
func AccountLabel(group string, key Key) string {
	if key.Label != "" {
		return key.Label
	}
	return group + " " + Fingerprint(key.APIKey)
}

// ClientLabel names a client key: its label, or for an unlabeled key
// its fingerprint.
func ClientLabel(key Key) string {
	if key.Label != "" {
		return key.Label
	}
	return Fingerprint(key.APIKey)
}

// ValidateKeyLabels checks each key's label and that no two keys share one.
// Errors name keys by the position name returns, never by value.
func ValidateKeyLabels(keys []Key, name func(int) string) error {
	seen := map[string]int{}
	for i, k := range keys {
		if err := ValidateLabel(k.Label); err != nil {
			return fmt.Errorf("%s: %w", name(i), err)
		}
		if k.Label == "" {
			continue
		}
		if first, taken := seen[k.Label]; taken {
			return fmt.Errorf("%s: label repeats the label of %s", name(i), name(first))
		}
		seen[k.Label] = i
	}
	return nil
}
