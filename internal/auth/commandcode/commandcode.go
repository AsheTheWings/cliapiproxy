// Package commandcode implements the Command Code gateway client: the account
// state and the model catalog behind an API key.
package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIURL is the gateway origin.
	DefaultAPIURL = "https://api.commandcode.ai"

	// RefreshInterval is how long an account's plan and catalog stay current
	// before a credential reloads them.
	RefreshInterval = time.Hour
	// RefreshLead is how long before that moment a credential refreshes.
	RefreshLead = time.Minute

	maxResponseBytes = 16 << 20
)

// subscriptionStatuses are the subscription states that grant the plan.
var subscriptionStatuses = map[string]bool{"active": true, "trialing": true, "past_due": true}

// APIError is an error response of the gateway.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("commandcode: status %d: %s", e.Status, detail)
}

// StatusCode returns the HTTP status of the response.
func (e *APIError) StatusCode() int { return e.Status }

// ParseAPIError builds the error of a non-2xx response body. It reads the
// shapes the gateway uses: {"success":false,"error":{code,message}} and
// {"error":{code,message}}; any other body is the message.
func ParseAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{Status: status}
	var shape struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &shape) == nil && shape.Error != nil {
		apiErr.Code = shape.Error.Code
		apiErr.Message = shape.Error.Message
		return apiErr
	}
	apiErr.Message = strings.TrimSpace(string(body))
	return apiErr
}

// Account is the plan and credit state of the account behind an API key.
type Account struct {
	UserID           string  `json:"user_id"`
	UserName         string  `json:"user_name,omitempty"`
	OrgID            string  `json:"org_id,omitempty"`
	PlanID           string  `json:"plan_id,omitempty"`
	PurchasedCredits float64 `json:"purchased_credits,omitempty"`
	FreeCredits      float64 `json:"free_credits,omitempty"`
}

// Client calls the gateway's account and catalog routes.
type Client struct {
	httpClient *http.Client
	apiURL     string
}

// NewClientForHost returns a client that sends its requests with httpClient to
// the gateway at apiURL.
func NewClientForHost(httpClient *http.Client, apiURL string) *Client {
	return &Client{httpClient: httpClient, apiURL: strings.TrimRight(apiURL, "/")}
}

// ApplyHeaders sets the identity headers the gateway requires on a request.
func ApplyHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("x-command-code-version", CLIVersion)
	req.Header.Set("x-cli-environment", CLIEnvironment)
}

func (c *Client) get(ctx context.Context, apiKey, path string, query url.Values) ([]byte, error) {
	endpoint := c.apiURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	ApplyHeaders(req, apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, ParseAPIError(resp.StatusCode, data)
	}
	return data, nil
}

// Account reads the identity, plan, and credits behind apiKey. A key the
// gateway rejects fails with an *APIError. When the plan or the credits cannot
// be read, the account has no plan and every model counts as available, as the
// CLI treats it.
func (c *Client) Account(ctx context.Context, apiKey string) (*Account, error) {
	data, err := c.get(ctx, apiKey, "/alpha/whoami", nil)
	if err != nil {
		return nil, whoamiError(err)
	}
	var who struct {
		User struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			UserName string `json:"userName"`
		} `json:"user"`
		Org *struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	if err = json.Unmarshal(data, &who); err != nil || strings.TrimSpace(who.User.ID) == "" {
		return nil, errors.New("commandcode: the gateway returned no user for this key")
	}
	account := &Account{UserID: strings.TrimSpace(who.User.ID), UserName: strings.TrimSpace(who.User.UserName)}
	if account.UserName == "" {
		account.UserName = strings.TrimSpace(who.User.Name)
	}
	query := url.Values{}
	if who.Org != nil && strings.TrimSpace(who.Org.ID) != "" {
		account.OrgID = strings.TrimSpace(who.Org.ID)
		query.Set("orgId", account.OrgID)
	}

	subscription, errSub := c.get(ctx, apiKey, "/alpha/billing/subscriptions", query)
	credits, errCredits := c.get(ctx, apiKey, "/alpha/billing/credits", query)
	if errSub != nil || errCredits != nil {
		return account, nil
	}
	var sub struct {
		Success bool `json:"success"`
		Data    struct {
			PlanID string `json:"planId"`
			Status string `json:"status"`
		} `json:"data"`
	}
	var bal struct {
		Credits *struct {
			PurchasedCredits float64 `json:"purchasedCredits"`
			FreeCredits      float64 `json:"freeCredits"`
		} `json:"credits"`
	}
	if json.Unmarshal(subscription, &sub) != nil || json.Unmarshal(credits, &bal) != nil || bal.Credits == nil {
		return account, nil
	}
	if sub.Success && subscriptionStatuses[sub.Data.Status] {
		account.PlanID = sub.Data.PlanID
	}
	account.PurchasedCredits = bal.Credits.PurchasedCredits
	account.FreeCredits = bal.Credits.FreeCredits
	return account, nil
}

func whoamiError(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return fmt.Errorf("commandcode: read account: %w", err)
}

// Catalog reads the model catalog the CLI reads.
func (c *Client) Catalog(ctx context.Context, apiKey string) (*Catalog, error) {
	data, err := c.get(ctx, apiKey, "/provider/v1/models", url.Values{"view": {"client"}})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, apiErr
		}
		return nil, fmt.Errorf("commandcode: read model catalog: %w", err)
	}
	return ParseCatalog(data)
}
