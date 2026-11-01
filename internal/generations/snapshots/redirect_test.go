//go:build component

package snapshots

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type redirectTransport struct {
	location string
	urls     []string
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
	return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {r.location}},
		Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func TestEveryFetcherRefusesRedirectsOutsideItsFixedEndpoints(t *testing.T) {
	for provider, fetcher := range Fetchers {
		for _, location := range []string{"/state-changing-endpoint", "https://attacker.invalid/steal"} {
			t.Run(provider+location, func(t *testing.T) {
				rt := &redirectTransport{location: location}
				policyCalls := 0
				client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
					policyCalls++
					return http.ErrUseLastResponse
				}}
				_, err := fetcher.Fetch(context.Background(), client, credential(provider))
				var responseErr *HTTPError
				if !errors.As(err, &responseErr) || responseErr.Status != http.StatusTemporaryRedirect {
					t.Fatalf("redirect outcome = %v, want a non-2xx usage error", err)
				}
				if len(rt.urls) == 0 || policyCalls != 0 {
					t.Fatal("the fetcher sent no request or delegated its redirect policy")
				}
				for _, got := range rt.urls {
					fixed := false
					for _, allowed := range fixedURLs[provider] {
						fixed = fixed || got == allowed
					}
					if !fixed {
						t.Fatalf("request escaped the fixed endpoint set: %s", got)
					}
				}
				_ = client.CheckRedirect(nil, nil)
				if policyCalls != 1 {
					t.Fatal("the caller's redirect policy was changed")
				}
			})
		}
	}
}
