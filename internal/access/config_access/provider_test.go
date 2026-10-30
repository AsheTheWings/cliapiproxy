//go:build unit

package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/AsheTheWings/cliapiproxy/sdk/access"
)

// Without client keys the proxy refuses every request rather than opening.
func TestNoClientKeysRefusesEveryRequest(t *testing.T) {
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{newProvider("", normalizeKeys(nil))})
	for name, header := range map[string]string{"no key": "", "any key": "Bearer some-key"} {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if result, err := manager.Authenticate(context.Background(), req); err == nil || err.HTTPStatusCode() != 401 {
			t.Errorf("%s: result %+v, err %v; want 401", name, result, err)
		}
	}
}

func TestClientKeysAdmitTheirHolders(t *testing.T) {
	p := newProvider("", normalizeKeys([]string{" k1 ", "k2"}))
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Api-Key", "k1")
	if result, err := p.Authenticate(context.Background(), req); err != nil || result.Principal != "k1" {
		t.Fatalf("result %+v, err %v; want k1 admitted", result, err)
	}
}
