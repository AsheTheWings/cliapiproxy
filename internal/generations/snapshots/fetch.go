package snapshots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
)

// Fetcher reads one provider's usage source for a credential. It sends
// requests only to its fixed provider URLs, with the credential's own
// token, and never calls an endpoint that bills or changes account state.
type Fetcher interface {
	Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error)
}

// HTTPError is a usage endpoint's non-2xx answer.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("usage endpoint answered %d", e.Status) }

// ErrUnparseable marks a usage response the fetcher cannot read.
var ErrUnparseable = errors.New("unparseable usage response")

// errNoToken marks a credential without the token its fetcher needs.
var errNoToken = errors.New("the credential holds no usage token")

// maxResponse bounds a usage response.
const maxResponse = 4 << 20

// call sends one request and returns its 2xx body.
func call(ctx context.Context, client *http.Client, method, url string, header http.Header, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &HTTPError{Status: resp.StatusCode}
	}
	return data, nil
}

// getJSON calls a URL and requires a JSON object back.
func getJSON(ctx context.Context, client *http.Client, method, url string, header http.Header, body []byte) (json.RawMessage, error) {
	data, err := call(ctx, client, method, url, header, body)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, ErrUnparseable
	}
	return json.RawMessage(trimmed), nil
}

// token returns the first non-empty credential value of the names, from
// the credential's attributes, then its metadata.
func token(auth *cliproxyauth.Auth, names ...string) string {
	for _, name := range names {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes[name]); v != "" {
				return v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata[name].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// rawOf combines named responses into one object for the snapshot's raw
// column, dropping the named secret fields at their top level.
func rawOf(parts map[string]json.RawMessage, secretFields ...string) json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, part := range parts {
		if len(part) == 0 {
			continue
		}
		if len(secretFields) > 0 {
			var object map[string]json.RawMessage
			if json.Unmarshal(part, &object) == nil {
				for _, field := range secretFields {
					delete(object, field)
				}
				part, _ = json.Marshal(object)
			}
		}
		out[name] = part
	}
	data, _ := json.Marshal(out)
	return data
}

// Fetchers maps a credential's provider to its fetcher.
var Fetchers = map[string]Fetcher{
	"kiro":        kiroFetcher{},
	"commandcode": commandCodeFetcher{},
	"claude":      claudeFetcher{},
	"codex":       codexFetcher{},
	"kimi":        kimiFetcher{url: "https://api.kimi.com/coding/v1/usages"},
	"kimi-ai":     kimiFetcher{url: "https://api.kimi.ai/coding/v1/usages"},
	"xai":         xaiFetcher{},
	"meta":        metaFetcher{},
	"devin":       devinFetcher{},
	"antigravity": antigravityFetcher{},
}
