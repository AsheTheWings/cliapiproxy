//go:build unit

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestScrubRemovesCredentials(t *testing.T) {
	for _, secret := range []string{
		"Authorization: Bearer abcdefgh12345678",
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl",
		"key sk-proj-ABCDEFGHIJKLMNOPQRSTUV",
		"Get https://generativelanguage.googleapis.com/v1/models?alt=sse&key=AIzaSyA-1234567890abcdefghijklmnopqrstu",
		"proxy http://user:hunter2@proxy.invalid:3128",
		`{"access_token":"ya29.a0AfH6SMB","refresh_token":"1//0gabc"}`,
		"refresh failed: client_secret=GOCSPX-abcdef&grant_type=refresh",
		"gh token ghp_abcdefghijklmnopqrstuvwxyz0123",
	} {
		scrubbed := Scrub(secret)
		for _, fragment := range []string{"abcdefgh12345678", "c2lnbmF0dXJl", "ABCDEFGHIJKLMNOPQRSTUV", "AIzaSyA", "hunter2", "ya29", "1//0gabc", "GOCSPX", "ghp_"} {
			if strings.Contains(scrubbed, fragment) {
				t.Errorf("Scrub(%q) = %q, keeps %q", secret, scrubbed, fragment)
			}
		}
	}
	if got := Scrub("upstream returned 429 for model gpt-6.1-sol"); got != "upstream returned 429 for model gpt-6.1-sol" {
		t.Fatalf("Scrub changed a message without credentials: %q", got)
	}
}

type captureHook struct{ entries []*logrus.Entry }

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(e *logrus.Entry) error {
	h.entries = append(h.entries, e)
	return nil
}

func TestScrubbingHookScrubsMessageAndFields(t *testing.T) {
	inner := &captureHook{}
	original := &logrus.Entry{Message: "using Bearer abcdefgh12345678", Data: logrus.Fields{
		"error": errors.New("POST https://x.invalid/?access_token=s3cr3t: EOF"),
		"url":   "https://u:p4ss@h.invalid/",
		"count": 3,
	}}
	if errFire := (scrubbingHook{inner: inner}).Fire(original); errFire != nil {
		t.Fatal(errFire)
	}
	got := inner.entries[0]
	rendered := got.Message + fmt.Sprint(got.Data)
	for _, secret := range []string{"abcdefgh12345678", "s3cr3t", "p4ss"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("exported entry %q keeps %q", rendered, secret)
		}
	}
	if got.Data["count"] != 3 || !strings.Contains(original.Message, "abcdefgh12345678") {
		t.Fatal("the hook must keep plain values and leave the original entry untouched")
	}
	if slices.Contains(ExportedLogLevels, logrus.DebugLevel) || slices.Contains(ExportedLogLevels, logrus.TraceLevel) {
		t.Fatal("debug and trace records must not be exported")
	}
}

type statusError int

func (e statusError) Error() string   { return "status" }
func (e statusError) StatusCode() int { return int(e) }

type codeError string

func (e codeError) Error() string     { return string(e) }
func (e codeError) ErrorCode() string { return string(e) }

func TestErrorClassIsStableAndCataloged(t *testing.T) {
	catalog, errCatalog := LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	for err, want := range map[error]string{
		context.Canceled: ErrorCanceled,
		fmt.Errorf("wrap: %w", context.DeadlineExceeded): ErrorTimeout,
		statusError(http.StatusUnauthorized):             ErrorUnauthorized,
		statusError(http.StatusTooManyRequests):          ErrorRateLimited,
		statusError(http.StatusBadRequest):               ErrorClient,
		statusError(http.StatusBadGateway):               ErrorUpstream,
		codeError("auth_not_found"):                      ErrorNoCredential,
		codeError("quota_exhausted"):                     ErrorRateLimited,
		errors.New("boom"):                               ErrorOther,
	} {
		got := ErrorClass(err)
		if got != want {
			t.Errorf("ErrorClass(%v) = %q, want %q", err, got, want)
		}
		if !catalog.ValueAllowed(string(AttrErrorType), got) {
			t.Errorf("error class %q is not in the catalog", got)
		}
	}
	if ErrorClass(nil) != "" {
		t.Fatal("success has no error class")
	}
}

func TestParseHeadersKeepsValuesOutOfErrors(t *testing.T) {
	headers, err := ParseHeaders("authorization=Basic%20dXNlcjpwYXNz, x-scope = a%2Cb")
	if err != nil || headers["authorization"] != "Basic dXNlcjpwYXNz" || headers["x-scope"] != "a,b" {
		t.Fatalf("headers = %v, err = %v", headers, err)
	}
	for _, raw := range []string{"authorization", "=v", "k=%zz-secret"} {
		_, err := ParseHeaders(raw)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("ParseHeaders(%q) error = %v", raw, err)
		}
	}
}

func TestCatalogListsEverySignalAttribute(t *testing.T) {
	catalog, errCatalog := LoadCatalog()
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	if catalog.Version < 1 {
		t.Fatal("the catalog must carry a version")
	}
	execute, _ := catalog.SpanAttributes("cliapiproxy.execute")
	server, _ := catalog.SpanAttributes("GET /v1/models")
	for attrs, keys := range map[*[]string][]string{
		&execute: {string(AttrOperation), string(AttrProvider), string(AttrErrorType)},
		&server:  {string(AttrHTTPMethod), string(AttrHTTPRoute), string(AttrHTTPStatusCode), string(AttrErrorType)},
	} {
		for _, key := range keys {
			if !slices.Contains(*attrs, key) {
				t.Errorf("catalog lacks attribute %s", key)
			}
		}
	}
}
