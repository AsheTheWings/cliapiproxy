package snapshots

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/auth/commandcode"
	"github.com/AsheTheWings/cliapiproxy/internal/auth/devin"
	kiroauth "github.com/AsheTheWings/cliapiproxy/internal/auth/kiro"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Fixed usage endpoints.
const (
	commandCodeAPIURL      = commandcode.DefaultAPIURL
	claudeUsageURL         = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL          = "https://chatgpt.com/backend-api/wham/usage"
	xaiWeeklyBillingURL    = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	xaiMonthlyBillingURL   = "https://cli-chat-proxy.grok.com/v1/billing"
	xaiUserURL             = "https://cli-chat-proxy.grok.com/v1/user?include=subscription"
	metaQuotaURL           = "https://api.meta.ai/muse-code/key"
	devinUserStatusURL     = devin.DefaultServerURL + devin.DevinGetUserStatusPath
	antigravityQuotaURL    = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	xaiGrokClientVersion   = "1.0.46"
	antigravityHubVersion  = "2.23.0"
	claudeCLIUserAgent     = "claude-cli/2.1.280 (external, cli)"
	codexTUIUserAgent      = "codex-tui/0.149.1"
	commandCodeOrgIDQuery  = "orgId"
	secondsPerFiveHours    = 5 * 3600
	secondsPerWeek         = 7 * 86400
	secondsPerDay          = 86400
	percentPerFraction     = 100
	centsPerDollar         = 100
	millisecondsThreshold  = 1e12
	usageTokenAttribute    = "api_key"
	accessTokenMetadataKey = "access_token"
)

// apiKey returns the API key a Kiro or Command Code credential sends.
func apiKey(auth *cliproxyauth.Auth) string {
	if auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey])
}

func bearer(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}, "Accept": {"application/json"}}
}

// num reads a JSON number or numeric string.
func num(v gjson.Result) (float64, bool) {
	switch v.Type {
	case gjson.Number:
		return v.Float(), true
	case gjson.String:
		f, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func optional(v gjson.Result) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

// instant reads an RFC 3339 time or a Unix time in seconds or milliseconds.
func instant(v gjson.Result) time.Time {
	if f, ok := num(v); ok && f > 0 {
		if f > millisecondsThreshold {
			return time.UnixMilli(int64(f)).UTC()
		}
		return time.Unix(int64(f), 0).UTC()
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v.String())); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func first(doc gjson.Result, paths ...string) gjson.Result {
	for _, p := range paths {
		if v := doc.Get(p); v.Exists() && v.Type != gjson.Null {
			return v
		}
	}
	return gjson.Result{}
}

// --- Kiro

type kiroFetcher struct{}

func (kiroFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := apiKey(auth)
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := bearer(tok)
	header.Set("Content-Type", "application/x-amz-json-1.0")
	header.Set("X-Amz-Target", "KiroControlPlaneBearerService.GetUsageLimits")
	header.Set("User-Agent", kiroauth.ControlPlaneUserAgent)
	header.Set("X-Amz-User-Agent", kiroauth.ControlPlaneAmzUserAgent)
	header.Set("X-Amzn-Codewhisperer-Optout", "true")
	header.Set("TokenType", kiroauth.TokenType)
	body, err := getJSON(ctx, client, http.MethodPost, kiroauth.DefaultManagementURL+"/", header, []byte(`{"origin":"`+kiroauth.Origin+`"}`))
	if err != nil {
		return Reading{}, err
	}
	return parseKiro(body)
}

func parseKiro(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"usage_limits": body})}
	title := strings.TrimSpace(doc.Get("subscriptionInfo.subscriptionTitle").String())
	kind := strings.TrimSpace(doc.Get("subscriptionInfo.type").String())
	switch {
	case title != "" && kind != "":
		r.Plan = title + " (" + kind + ")"
	default:
		r.Plan = title + kind
	}
	for _, entry := range doc.Get("usageBreakdownList").Array() {
		name := strings.ToLower(strings.TrimSpace(entry.Get("resourceType").String()))
		if name == "" {
			name = "usage"
		}
		used := optional(first(entry, "currentUsageWithPrecision", "currentUsage"))
		limit := optional(first(entry, "usageLimitWithPrecision", "usageLimit"))
		m := Meter{Meter: name, Unit: UnitCredits, Used: used, Limit: limit, ResetsAt: instant(entry.Get("nextDateReset"))}
		if used != nil && limit != nil {
			m.Remaining = ptr(math.Max(0, *limit-*used))
		}
		r.Meters = append(r.Meters, m)
		if overage := optional(entry.Get("overageCap")); overage != nil {
			r.Meters = append(r.Meters, Meter{Meter: name + "_overage_cap", Unit: UnitCredits, Limit: overage, ResetsAt: m.ResetsAt})
		}
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Command Code

type commandCodeFetcher struct{}

func commandCodeHeader(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}, "Accept": {"application/json"}, "User-Agent": {commandcode.UserAgent},
		"X-Command-Code-Version": {commandcode.CLIVersion}, "X-Cli-Environment": {commandcode.CLIEnvironment}}
}

func (commandCodeFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := apiKey(auth)
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := commandCodeHeader(tok)
	who, err := getJSON(ctx, client, http.MethodGet, commandCodeAPIURL+"/alpha/whoami", header, nil)
	if err != nil {
		return Reading{}, err
	}
	query := ""
	if org := strings.TrimSpace(gjson.GetBytes(who, "org.id").String()); org != "" {
		query = "?" + url.Values{commandCodeOrgIDQuery: {org}}.Encode()
	}
	subscription, err := getJSON(ctx, client, http.MethodGet, commandCodeAPIURL+"/alpha/billing/subscriptions"+query, header, nil)
	if err != nil {
		return Reading{}, err
	}
	credits, err := getJSON(ctx, client, http.MethodGet, commandCodeAPIURL+"/alpha/billing/credits"+query, header, nil)
	if err != nil {
		return Reading{}, err
	}
	return parseCommandCode(subscription, credits)
}

func parseCommandCode(subscription, credits json.RawMessage) (Reading, error) {
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"subscriptions": subscription, "credits": credits})}
	sub := gjson.ParseBytes(subscription)
	plan := strings.TrimSpace(first(sub, "data.planId", "planId").String())
	if status := strings.TrimSpace(first(sub, "data.status", "status").String()); plan != "" && status != "" {
		plan += " (" + status + ")"
	}
	r.Plan = plan
	doc := gjson.ParseBytes(credits)
	for _, balance := range []struct{ meter, path string }{
		{"monthly", "credits.monthlyCredits"}, {"purchased", "credits.purchasedCredits"}, {"free", "credits.freeCredits"},
	} {
		if v := optional(doc.Get(balance.path)); v != nil {
			r.Meters = append(r.Meters, Meter{Meter: balance.meter, Unit: UnitCredits, Remaining: v})
		}
	}
	for _, window := range []struct {
		meter, path string
		seconds     int64
	}{{"five_hour", "windowLimits.fiveHour", secondsPerFiveHours}, {"weekly", "windowLimits.weekly", secondsPerWeek}} {
		w := doc.Get(window.path)
		if !w.IsObject() {
			continue
		}
		m := Meter{Meter: window.meter, Unit: UnitCredits, Used: optional(w.Get("used")), Limit: optional(w.Get("cap")),
			WindowSeconds: window.seconds, ResetsAt: instant(w.Get("resetAt"))}
		if m.Used != nil && m.Limit != nil {
			m.Remaining = ptr(math.Max(0, *m.Limit-*m.Used))
		}
		r.Meters = append(r.Meters, m)
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Claude

type claudeFetcher struct{}

func (claudeFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, accessTokenMetadataKey)
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := bearer(tok)
	header.Set("User-Agent", claudeCLIUserAgent)
	header.Set("anthropic-beta", "oauth-2025-04-20")
	body, err := getJSON(ctx, client, http.MethodGet, claudeUsageURL, header, nil)
	if err != nil {
		return Reading{}, err
	}
	return parseClaude(body)
}

// claudeWindows are the windows of Claude's OAuth usage, with their length.
var claudeWindows = []struct {
	key     string
	seconds int64
}{{"five_hour", secondsPerFiveHours}, {"seven_day", secondsPerWeek}, {"seven_day_opus", secondsPerWeek}, {"seven_day_sonnet", secondsPerWeek},
	{"seven_day_oauth_apps", secondsPerWeek}, {"seven_day_cowork", secondsPerWeek}}

func parseClaude(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"usage": body})}
	for _, w := range claudeWindows {
		v := doc.Get(w.key)
		if !v.IsObject() {
			continue
		}
		if used := optional(v.Get("utilization")); used != nil {
			r.Meters = append(r.Meters, Meter{Meter: w.key, Unit: UnitPercent, Used: used, WindowSeconds: w.seconds, ResetsAt: instant(v.Get("resets_at"))})
		}
		if limit := optional(v.Get("limit_dollars")); limit != nil {
			r.Meters = append(r.Meters, Meter{Meter: w.key, Scope: "dollars", Unit: UnitUSD, Limit: limit, Used: optional(v.Get("used_dollars")),
				Remaining: optional(v.Get("remaining_dollars")), WindowSeconds: w.seconds, ResetsAt: instant(v.Get("resets_at"))})
		}
	}
	if extra := doc.Get("extra_usage"); extra.IsObject() && extra.Get("is_enabled").Bool() {
		m := Meter{Meter: "extra_usage", Unit: UnitCredits, Used: optional(extra.Get("used_credits")), Limit: optional(extra.Get("monthly_limit"))}
		if m.Used != nil && m.Limit != nil {
			m.Remaining = ptr(math.Max(0, *m.Limit-*m.Used))
		}
		r.Meters = append(r.Meters, m)
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Codex

type codexFetcher struct{}

func (codexFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, accessTokenMetadataKey)
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := bearer(tok)
	header.Set("User-Agent", codexTUIUserAgent)
	if account := token(auth, "account_id"); account != "" {
		header.Set("ChatGPT-Account-Id", account)
	}
	body, err := getJSON(ctx, client, http.MethodGet, codexUsageURL, header, nil)
	if err != nil {
		return Reading{}, err
	}
	return parseCodex(body)
}

func codexWindows(r *Reading, scope string, limit gjson.Result) {
	for _, name := range []string{"primary", "secondary"} {
		w := first(limit, name+"_window", name+"Window")
		used := optional(first(w, "used_percent", "usedPercent"))
		if used == nil {
			continue
		}
		m := Meter{Meter: name, Scope: scope, Unit: UnitPercent, Used: used, ResetsAt: instant(first(w, "reset_at", "resetAt"))}
		if seconds, ok := num(first(w, "limit_window_seconds", "limitWindowSeconds")); ok && seconds > 0 {
			m.WindowSeconds = int64(seconds)
		}
		r.Meters = append(r.Meters, m)
	}
}

func parseCodex(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"usage": body}), Plan: strings.TrimSpace(first(doc, "plan_type", "planType").String())}
	codexWindows(&r, "", first(doc, "rate_limit", "rateLimit"))
	codexWindows(&r, "code_review", first(doc, "code_review_rate_limit", "codeReviewRateLimit"))
	for _, extra := range first(doc, "additional_rate_limits", "additionalRateLimits").Array() {
		name := strings.TrimSpace(first(extra, "limit_name", "limitName", "metered_feature", "meteredFeature").String())
		if name != "" {
			codexWindows(&r, name, first(extra, "rate_limit", "rateLimit"))
		}
	}
	if balance := optional(doc.Get("credits.balance")); balance != nil {
		r.Meters = append(r.Meters, Meter{Meter: "credits", Unit: UnitCredits, Remaining: balance})
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Kimi

type kimiFetcher struct{ url string }

func (f kimiFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, accessTokenMetadataKey)
	if tok == "" {
		return Reading{}, errNoToken
	}
	body, err := getJSON(ctx, client, http.MethodGet, f.url, bearer(tok), nil)
	if err != nil {
		return Reading{}, err
	}
	return parseKimi(body)
}

// kimiUnitSeconds are the seconds of Kimi's window time units.
var kimiUnitSeconds = map[string]int64{"second": 1, "minute": 60, "hour": 3600, "day": secondsPerDay, "week": secondsPerWeek}

func kimiMeter(name string, detail gjson.Result) (Meter, bool) {
	m := Meter{Meter: name, Unit: UnitRequests, Used: optional(detail.Get("used")), Limit: optional(detail.Get("limit")),
		Remaining: optional(detail.Get("remaining")), ResetsAt: instant(first(detail, "resetAt", "reset_at", "resetTime", "reset_time"))}
	return m, m.Used != nil || m.Remaining != nil
}

func parseKimi(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"usages": body})}
	if m, ok := kimiMeter("usage", doc.Get("usage")); ok {
		r.Meters = append(r.Meters, m)
	}
	for _, item := range doc.Get("limits").Array() {
		detail := item.Get("detail")
		if !detail.IsObject() {
			detail = item
		}
		duration, _ := num(first(item, "window.duration", "duration", "detail.duration"))
		unit := strings.ToLower(strings.TrimPrefix(strings.ToUpper(first(item, "window.timeUnit", "timeUnit").String()), "TIME_UNIT_"))
		unit = strings.TrimSuffix(unit, "s")
		seconds := int64(duration) * kimiUnitSeconds[unit]
		name := "limit"
		if seconds > 0 {
			name = "limit_" + strconv.FormatInt(seconds, 10) + "s"
		}
		if m, ok := kimiMeter(name, detail); ok {
			m.WindowSeconds = seconds
			r.Meters = append(r.Meters, m)
		}
	}
	if ratio := optional(doc.Get("usages.limit_month_total.used_ratio")); ratio != nil {
		r.Meters = append(r.Meters, Meter{Meter: "month_total", Unit: UnitPercent, Used: ptr(*ratio * percentPerFraction),
			ResetsAt: instant(doc.Get("usages.limit_month_total.reset_time"))})
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- xAI

type xaiFetcher struct{}

func xaiHeader(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}, "X-Xai-Token-Auth": {"xai-grok-cli"},
		"X-Grok-Client-Version": {xaiGrokClientVersion}, "Accept": {"*/*"},
		"User-Agent": {"grok-pager/" + xaiGrokClientVersion + " grok-shell/" + xaiGrokClientVersion + " (macos; aarch64)"}}
}

func (xaiFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, accessTokenMetadataKey)
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := xaiHeader(tok)
	weekly, err := getJSON(ctx, client, http.MethodGet, xaiWeeklyBillingURL, header, nil)
	if err != nil {
		return Reading{}, err
	}
	monthly, err := getJSON(ctx, client, http.MethodGet, xaiMonthlyBillingURL, header, nil)
	if err != nil {
		return Reading{}, err
	}
	// The plan is optional: a failed user read keeps the billing readings.
	user, _ := getJSON(ctx, client, http.MethodGet, xaiUserURL, header, nil)
	return parseXAI(weekly, monthly, user)
}

func cents(v gjson.Result) *float64 {
	if v.IsObject() {
		v = v.Get("val")
	}
	if f, ok := num(v); ok {
		return ptr(f / centsPerDollar)
	}
	return nil
}

func xaiPeriodSeconds(config gjson.Result) int64 {
	period := first(config, "currentPeriod", "current_period")
	start, end := instant(period.Get("start")), instant(period.Get("end"))
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return int64(end.Sub(start).Seconds())
}

func parseXAI(weekly, monthly, user json.RawMessage) (Reading, error) {
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"weekly_billing": weekly, "monthly_billing": monthly, "user": user})}
	if len(user) > 0 {
		u := gjson.ParseBytes(user)
		r.Plan = strings.TrimSpace(first(u, "subscriptionTier", "subscription_tier", "user.subscriptionTier", "subscription.tier").String())
	}
	for _, part := range []struct {
		meter string
		body  json.RawMessage
	}{{"weekly", weekly}, {"monthly", monthly}} {
		config := gjson.GetBytes(part.body, "config")
		if !config.IsObject() {
			continue
		}
		end := instant(first(config, "currentPeriod.end", "current_period.end", "billingPeriodEnd", "billing_period_end"))
		seconds := xaiPeriodSeconds(config)
		if used := optional(first(config, "creditUsagePercent", "credit_usage_percent")); used != nil {
			r.Meters = append(r.Meters, Meter{Meter: part.meter, Unit: UnitPercent, Used: used, WindowSeconds: seconds, ResetsAt: end})
		}
		if limit := cents(first(config, "monthlyLimit", "monthly_limit")); limit != nil && *limit > 0 {
			r.Meters = append(r.Meters, Meter{Meter: part.meter, Scope: "spend", Unit: UnitUSD, Limit: limit, Used: cents(config.Get("used")),
				WindowSeconds: seconds, ResetsAt: end})
		}
		if limit := cents(first(config, "onDemandCap", "on_demand_cap")); limit != nil && *limit > 0 {
			r.Meters = append(r.Meters, Meter{Meter: part.meter, Scope: "on_demand", Unit: UnitUSD, Limit: limit,
				Used: cents(first(config, "onDemandUsed", "on_demand_used")), WindowSeconds: seconds, ResetsAt: end})
		}
		if balance := cents(first(config, "prepaidBalance", "prepaid_balance")); balance != nil && part.meter == "monthly" {
			r.Meters = append(r.Meters, Meter{Meter: "prepaid", Unit: UnitUSD, Remaining: balance})
		}
		for _, product := range first(config, "productUsage", "product_usage").Array() {
			name := strings.TrimSpace(product.Get("product").String())
			if used := optional(first(product, "usagePercent", "usage_percent")); name != "" && used != nil {
				r.Meters = append(r.Meters, Meter{Meter: part.meter, Scope: name, Unit: UnitPercent, Used: used, WindowSeconds: seconds, ResetsAt: end})
			}
		}
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Meta

type metaFetcher struct{}

func (metaFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, "dca_token")
	if !strings.HasPrefix(tok, "dca:") {
		return Reading{}, errNoToken
	}
	header := bearer(tok)
	header.Set("Content-Type", "application/json")
	header.Set("X-Api-Version", "1.0.0")
	// The route returns the account's existing key with its usage.
	body, err := getJSON(ctx, client, http.MethodPost, metaQuotaURL, header, []byte(`{}`))
	if err != nil {
		return Reading{}, err
	}
	return parseMeta(body)
}

func parseMeta(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	// The response carries the account's API key, which raw never keeps.
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"key": body}, "api_key", "key")}
	r.Plan = strings.TrimSpace(first(doc, "subs_tier_name", "subs_usage.tier").String())
	for _, w := range []string{"window", "weekly"} {
		v := doc.Get("subs_usage." + w)
		used := optional(v.Get("used_percent"))
		if used == nil {
			continue
		}
		m := Meter{Meter: w, Unit: UnitPercent, Used: ptr(math.Min(100, math.Max(0, *used))), ResetsAt: instant(v.Get("resets_at"))}
		if minutes, ok := num(v.Get("window_duration_mins")); ok && minutes > 0 {
			m.WindowSeconds = int64(minutes * 60)
		} else if w == "weekly" {
			m.WindowSeconds = secondsPerWeek
		}
		r.Meters = append(r.Meters, m)
	}
	if !doc.IsObject() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}

// --- Devin

type devinFetcher struct{}

func (devinFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, usageTokenAttribute, "session_token", "token")
	if tok == "" {
		return Reading{}, errNoToken
	}
	header := http.Header{"Authorization": {"Basic " + tok + "-" + tok}, "Connect-Protocol-Version": {"1"},
		"Content-Type": {"application/proto"}, "Accept": {"*/*"}, "User-Agent": {""}}
	body := devin.BuildGetUserStatusRequest(tok, devin.GenerateDeviceFingerprint(token(auth, "device_seed")))
	data, err := call(ctx, client, http.MethodPost, devinUserStatusURL, header, body)
	if err != nil {
		return Reading{}, err
	}
	return parseDevin(data)
}

func parseDevin(data []byte) (Reading, error) {
	status, err := devin.ParseGetUserStatusResponse(data)
	if err != nil || status == nil {
		return Reading{}, ErrUnparseable
	}
	// The status message holds the account's email and IDs, so raw keeps
	// only the readings.
	r := Reading{Plan: status.Plan}
	r.Meters = append(r.Meters,
		Meter{Meter: "daily", Unit: UnitPercent, Remaining: ptr(float64(status.DailyQuotaRemainingPercent)), WindowSeconds: secondsPerDay, ResetsAt: status.DailyQuotaResetAt.UTC()},
		Meter{Meter: "weekly", Unit: UnitPercent, Remaining: ptr(float64(status.WeeklyQuotaRemainingPercent)), WindowSeconds: secondsPerWeek, ResetsAt: status.WeeklyQuotaResetAt.UTC()})
	for i := range r.Meters {
		if r.Meters[i].ResetsAt.Unix() <= 0 {
			r.Meters[i].ResetsAt = time.Time{}
		}
	}
	return r, nil
}

// --- Antigravity

type antigravityFetcher struct{}

func (antigravityFetcher) Fetch(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth) (Reading, error) {
	tok := token(auth, accessTokenMetadataKey)
	project := token(auth, "project_id")
	if tok == "" || project == "" {
		return Reading{}, errNoToken
	}
	header := bearer(tok)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", "antigravity/hub/"+antigravityHubVersion+" darwin/arm64")
	request, _ := json.Marshal(map[string]string{"project": project})
	body, err := getJSON(ctx, client, http.MethodPost, antigravityQuotaURL, header, request)
	if err != nil {
		return Reading{}, err
	}
	return parseAntigravity(body)
}

func parseAntigravity(body json.RawMessage) (Reading, error) {
	doc := gjson.ParseBytes(body)
	r := Reading{Raw: rawOf(map[string]json.RawMessage{"quota_summary": body})}
	for gi, group := range doc.Get("groups").Array() {
		scope := strings.TrimSpace(first(group, "displayName", "display_name").String())
		if scope == "" {
			scope = "group_" + strconv.Itoa(gi+1)
		}
		for bi, bucket := range group.Get("buckets").Array() {
			fraction := optional(first(bucket, "remainingFraction", "remaining_fraction"))
			if fraction == nil {
				continue
			}
			name := strings.TrimSpace(first(bucket, "bucketId", "bucket_id", "window").String())
			if name == "" {
				name = "bucket_" + strconv.Itoa(bi+1)
			}
			r.Meters = append(r.Meters, Meter{Meter: name, Scope: scope, Unit: UnitPercent, Remaining: ptr(*fraction * percentPerFraction),
				ResetsAt: instant(first(bucket, "resetTime", "reset_time"))})
		}
	}
	if r.Empty() {
		return Reading{}, ErrUnparseable
	}
	return r, nil
}
