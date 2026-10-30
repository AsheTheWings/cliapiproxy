//go:build component

package commandcode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type gateway struct {
	mu       sync.Mutex
	requests []string
	failures map[string]int
}

func newGateway(t *testing.T, org string, failures map[string]int) (*Client, *gateway) {
	t.Helper()
	gw := &gateway{failures: failures}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.mu.Lock()
		gw.requests = append(gw.requests, r.URL.RequestURI())
		gw.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer key-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","status":401,"message":"Invalid 'Authorization' header or token."}}`))
			return
		}
		if r.Header.Get("x-command-code-version") != CLIVersion || r.Header.Get("User-Agent") != UserAgent ||
			r.Header.Get("x-cli-environment") != CLIEnvironment {
			t.Errorf("identity headers = %v", r.Header)
		}
		if status := gw.failures[r.URL.Path]; status != 0 {
			w.WriteHeader(status)
			return
		}
		switch r.URL.Path {
		case "/alpha/whoami":
			if org == "" {
				_, _ = w.Write([]byte(`{"success":true,"user":{"id":"user-1","name":"Ada","userName":"ada"},"org":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"user":{"id":"user-1","name":"Ada","userName":"ada"},"org":{"id":"` + org + `"}}`))
		case "/alpha/billing/subscriptions":
			_, _ = w.Write([]byte(`{"success":true,"data":{"planId":"individual-goat","status":"active"}}`))
		case "/alpha/billing/credits":
			_, _ = w.Write([]byte(`{"credits":{"monthlyCredits":9.7,"purchasedCredits":2.5,"freeCredits":0}}`))
		case "/provider/v1/models":
			if r.URL.Query().Get("view") != "client" {
				t.Errorf("catalog view = %q", r.URL.Query().Get("view"))
			}
			_, _ = w.Write([]byte(`{"catalogVersion":"v9","data":[{"id":"a/b","category":"opensource"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return NewClientForHost(server.Client(), server.URL), gw
}

func TestAccountReadsIdentityPlanAndCredits(t *testing.T) {
	client, gw := newGateway(t, "", nil)
	account, err := client.Account(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	want := Account{UserID: "user-1", UserName: "ada", PlanID: "individual-goat", PurchasedCredits: 2.5}
	if *account != want {
		t.Errorf("account = %+v, want %+v", *account, want)
	}
	if len(gw.requests) != 3 {
		t.Errorf("requests = %v", gw.requests)
	}
}

func TestAccountQueriesBillingByOrganization(t *testing.T) {
	client, gw := newGateway(t, "org-7", nil)
	account, err := client.Account(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if account.OrgID != "org-7" {
		t.Errorf("org = %q", account.OrgID)
	}
	for _, request := range gw.requests[1:] {
		if request[len(request)-len("orgId=org-7"):] != "orgId=org-7" {
			t.Errorf("billing request %q does not name the organization", request)
		}
	}
}

func TestAccountWithoutBillingHasNoPlan(t *testing.T) {
	client, _ := newGateway(t, "", map[string]int{"/alpha/billing/credits": http.StatusInternalServerError})
	account, err := client.Account(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if account.PlanID != "" || account.UserID != "user-1" {
		t.Errorf("account = %+v, want an identity with no plan", *account)
	}
}

func TestAccountReportsARejectedKey(t *testing.T) {
	client, _ := newGateway(t, "", nil)
	_, err := client.Account(context.Background(), "wrong")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("error = %v", err)
	}
}

func TestCatalogReadsTheClientView(t *testing.T) {
	client, _ := newGateway(t, "", nil)
	catalog, err := client.Catalog(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != "v9" || len(catalog.Models) != 1 || catalog.Models[0].ID != "a/b" {
		t.Errorf("catalog = %+v", catalog)
	}
}

func TestParseAPIErrorReadsEveryShape(t *testing.T) {
	cases := []struct {
		status        int
		body          string
		code, message string
	}{
		{401, `{"success":false,"error":{"code":"UNAUTHORIZED","status":401,"message":"bad key"}}`, "UNAUTHORIZED", "bad key"},
		{403, `{"error":{"code":"upgrade_required","message":"too old","minVersion":"0.18.10"}}`, "upgrade_required", "too old"},
		{403, `{"error":{"message":"no api","type":"permission_error","code":"upgrade_required"}}`, "upgrade_required", "no api"},
		{403, "error code: 1010\n", "", "error code: 1010"},
	}
	for _, tc := range cases {
		got := ParseAPIError(tc.status, []byte(tc.body))
		if got.Status != tc.status || got.Code != tc.code || got.Message != tc.message {
			t.Errorf("ParseAPIError(%q) = %+v", tc.body, got)
		}
	}
}
