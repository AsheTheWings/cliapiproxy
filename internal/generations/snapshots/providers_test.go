//go:build unit

package snapshots

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// meter is a meter's expected values, with nil for a value not reported.
type meter struct {
	unit                   string
	used, limit, remaining *float64
	window                 int64
	reset                  int64
}

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

// check compares a reading's plan and meters, keyed by meter and scope.
func check(t *testing.T, got Reading, err error, plan string, want map[string]meter) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan != plan {
		t.Errorf("plan = %q, want %q", got.Plan, plan)
	}
	if len(got.Meters) != len(want) {
		t.Errorf("%d meters, want %d: %+v", len(got.Meters), len(want), got.Meters)
	}
	same := func(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, m := range got.Meters {
		key := m.Meter
		if m.Scope != "" {
			key += "/" + m.Scope
		}
		w, ok := want[key]
		if !ok {
			t.Errorf("unexpected meter %s", key)
			continue
		}
		var reset int64
		if !m.ResetsAt.IsZero() {
			reset = m.ResetsAt.Unix()
		}
		if m.Unit != w.unit || !same(m.Used, w.used) || !same(m.Limit, w.limit) || !same(m.Remaining, w.remaining) ||
			m.WindowSeconds != w.window || reset != w.reset {
			t.Errorf("meter %s = %+v, want %+v", key, m, w)
		}
	}
}

func TestParseKiro(t *testing.T) {
	r, err := parseKiro(json.RawMessage(`{"subscriptionInfo":{"subscriptionTitle":"KIRO PRO+","type":"Q_DEVELOPER_STANDALONE_PRO_PLUS"},
		"usageBreakdownList":[{"resourceType":"CREDIT","usageLimit":2000,"currentUsageWithPrecision":123.45,"overageCap":500,"nextDateReset":1793491200}]}`))
	check(t, r, err, "KIRO PRO+ (Q_DEVELOPER_STANDALONE_PRO_PLUS)", map[string]meter{
		"credit":             {unit: UnitCredits, used: ptr(123.45), limit: ptr(2000), remaining: ptr(2000 - 123.45), reset: 1793491200},
		"credit_overage_cap": {unit: UnitCredits, limit: ptr(500), reset: 1793491200},
	})
}

func TestParseCommandCode(t *testing.T) {
	r, err := parseCommandCode(json.RawMessage(`{"planId":"pro","status":"active"}`),
		json.RawMessage(`{"credits":{"monthlyCredits":800,"purchasedCredits":50,"freeCredits":5},
		"windowLimits":{"fiveHour":{"used":30,"cap":120,"resetAt":1793491200000},"weekly":{"used":200,"cap":900,"resetAt":1793836800000}}}`))
	check(t, r, err, "pro (active)", map[string]meter{
		"monthly":   {unit: UnitCredits, remaining: ptr(800)},
		"purchased": {unit: UnitCredits, remaining: ptr(50)},
		"free":      {unit: UnitCredits, remaining: ptr(5)},
		"five_hour": {unit: UnitCredits, used: ptr(30), limit: ptr(120), remaining: ptr(90), window: secondsPerFiveHours, reset: 1793491200},
		"weekly":    {unit: UnitCredits, used: ptr(200), limit: ptr(900), remaining: ptr(700), window: secondsPerWeek, reset: 1793836800},
	})
}

func TestParseClaude(t *testing.T) {
	r, err := parseClaude(json.RawMessage(`{"five_hour":{"utilization":25,"resets_at":"2026-11-01T05:00:00Z"},
		"seven_day":{"utilization":40,"resets_at":"2026-11-05T00:00:00Z"},"seven_day_opus":{"utilization":10},
		"seven_day_sonnet":null,"extra_usage":{"is_enabled":true,"monthly_limit":5000,"used_credits":1200}}`))
	check(t, r, err, "", map[string]meter{
		"five_hour":      {unit: UnitPercent, used: ptr(25), window: secondsPerFiveHours, reset: at(1793509200).Unix()},
		"seven_day":      {unit: UnitPercent, used: ptr(40), window: secondsPerWeek, reset: at(1793836800).Unix()},
		"seven_day_opus": {unit: UnitPercent, used: ptr(10), window: secondsPerWeek},
		"extra_usage":    {unit: UnitCredits, used: ptr(1200), limit: ptr(5000), remaining: ptr(3800)},
	})
}

func TestParseCodex(t *testing.T) {
	r, err := parseCodex(json.RawMessage(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":30,"limit_window_seconds":18000,"reset_at":1793509200},
		"secondary_window":{"used_percent":55,"limit_window_seconds":604800,"reset_at":1793836800}},
		"code_review_rate_limit":{"primary_window":{"used_percent":5,"limit_window_seconds":604800}},"credits":{"balance":"12.5"}}`))
	check(t, r, err, "plus", map[string]meter{
		"primary":             {unit: UnitPercent, used: ptr(30), window: 18000, reset: 1793509200},
		"secondary":           {unit: UnitPercent, used: ptr(55), window: 604800, reset: 1793836800},
		"primary/code_review": {unit: UnitPercent, used: ptr(5), window: 604800},
		"credits":             {unit: UnitCredits, remaining: ptr(12.5)},
	})
}

func TestParseKimi(t *testing.T) {
	r, err := parseKimi(json.RawMessage(`{"usage":{"limit":"1000","used":"100","remaining":"900","resetTime":"2026-11-08T00:00:00Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"60","resetTime":"2026-11-01T05:00:00Z"}}]}`))
	check(t, r, err, "", map[string]meter{
		"usage":        {unit: UnitRequests, used: ptr(100), limit: ptr(1000), remaining: ptr(900), reset: at(1794096000).Unix()},
		"limit_18000s": {unit: UnitRequests, limit: ptr(100), remaining: ptr(60), window: 18000, reset: at(1793509200).Unix()},
	})
}

func TestParseXAI(t *testing.T) {
	weekly := `{"config":{"creditUsagePercent":20,"currentPeriod":{"start":"2026-10-29T00:00:00Z","end":"2026-11-05T00:00:00Z"},
		"productUsage":[{"product":"grok-code","usagePercent":15}]}}`
	monthly := `{"config":{"monthlyLimit":{"val":5000},"used":{"val":1250},"prepaidBalance":{"val":300},
		"currentPeriod":{"start":"2026-10-01T00:00:00Z","end":"2026-11-01T00:00:00Z"}}}`
	r, err := parseXAI(json.RawMessage(weekly), json.RawMessage(monthly), json.RawMessage(`{"subscriptionTier":"SuperGrok"}`))
	check(t, r, err, "SuperGrok", map[string]meter{
		"weekly":           {unit: UnitPercent, used: ptr(20), window: secondsPerWeek, reset: at(1793836800).Unix()},
		"weekly/grok-code": {unit: UnitPercent, used: ptr(15), window: secondsPerWeek, reset: at(1793836800).Unix()},
		"monthly/spend":    {unit: UnitUSD, used: ptr(12.5), limit: ptr(50), window: 31 * secondsPerDay, reset: at(1793491200).Unix()},
		"prepaid":          {unit: UnitUSD, remaining: ptr(3)},
	})
}

func TestParseMetaKeepsNoKey(t *testing.T) {
	r, err := parseMeta(json.RawMessage(`{"api_key":"synthetic-meta-key","subs_tier_name":"Plus",
		"subs_usage":{"window":{"used_percent":12,"window_duration_mins":300,"resets_at":1793509200},"weekly":{"used_percent":140}}}`))
	check(t, r, err, "Plus", map[string]meter{
		"window": {unit: UnitPercent, used: ptr(12), window: 18000, reset: 1793509200},
		"weekly": {unit: UnitPercent, used: ptr(100), window: secondsPerWeek},
	})
	if strings.Contains(string(r.Raw), "synthetic-meta-key") {
		t.Fatalf("raw keeps the account's key: %s", r.Raw)
	}
}

func TestParseDevin(t *testing.T) {
	var plan, status, user, resp []byte
	plan = protowire.AppendTag(plan, 2, protowire.BytesType)
	plan = protowire.AppendString(plan, "Pro")
	status = protowire.AppendTag(status, 1, protowire.BytesType)
	status = protowire.AppendBytes(status, plan)
	for _, field := range []struct {
		num   protowire.Number
		value uint64
	}{{14, 70}, {15, 40}, {17, 1793491200}, {18, 1793836800}} {
		status = protowire.AppendTag(status, field.num, protowire.VarintType)
		status = protowire.AppendVarint(status, field.value)
	}
	user = protowire.AppendTag(user, 7, protowire.BytesType)
	user = protowire.AppendString(user, "someone@example.com")
	user = protowire.AppendTag(user, 13, protowire.BytesType)
	user = protowire.AppendBytes(user, status)
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, user)
	r, err := parseDevin(resp)
	check(t, r, err, "Pro", map[string]meter{
		"daily":  {unit: UnitPercent, remaining: ptr(70), window: secondsPerDay, reset: 1793491200},
		"weekly": {unit: UnitPercent, remaining: ptr(40), window: secondsPerWeek, reset: 1793836800},
	})
	if len(r.Raw) != 0 {
		t.Fatalf("raw keeps the account's identity: %s", r.Raw)
	}
}

func TestParseAntigravity(t *testing.T) {
	r, err := parseAntigravity(json.RawMessage(`{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"daily","remainingFraction":0.75,"resetTime":"2026-11-02T00:00:00Z"}]}]}`))
	check(t, r, err, "", map[string]meter{
		"daily/Gemini": {unit: UnitPercent, remaining: ptr(75), reset: at(1793577600).Unix()},
	})
}

func TestUnparseableBodiesAreRefused(t *testing.T) {
	for name, parse := range map[string]func() (Reading, error){
		"kiro":        func() (Reading, error) { return parseKiro(json.RawMessage(`{}`)) },
		"claude":      func() (Reading, error) { return parseClaude(json.RawMessage(`{"unrelated":1}`)) },
		"codex":       func() (Reading, error) { return parseCodex(json.RawMessage(`{}`)) },
		"antigravity": func() (Reading, error) { return parseAntigravity(json.RawMessage(`{"groups":[]}`)) },
		"devin":       func() (Reading, error) { return parseDevin([]byte{0xff}) },
	} {
		if _, err := parse(); !errors.Is(err, ErrUnparseable) {
			t.Errorf("%s: err = %v, want ErrUnparseable", name, err)
		}
	}
}

func TestResponseHeaders(t *testing.T) {
	claude := http.Header{}
	claude.Set("anthropic-ratelimit-unified-5h-utilization", "0.25")
	claude.Set("anthropic-ratelimit-unified-5h-reset", "1793509200")
	claude.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	r, ok := FromClaudeHeaders(claude)
	if !ok {
		t.Fatal("no Claude reading")
	}
	check(t, r, nil, "", map[string]meter{
		"five_hour": {unit: UnitPercent, used: ptr(25), window: secondsPerFiveHours, reset: 1793509200},
		"seven_day": {unit: UnitPercent, used: ptr(50), window: secondsPerWeek},
	})

	codex := http.Header{}
	codex.Set("X-Codex-Plan-Type", "pro")
	codex.Set("X-Codex-Primary-Used-Percent", "30")
	codex.Set("X-Codex-Primary-Window-Minutes", "300")
	codex.Set("X-Codex-Primary-Reset-At", "1793509200")
	codex.Set("X-Codex-Secondary-Used-Percent", "60")
	codex.Set("X-Codex-Secondary-Reset-After-Seconds", "3600")
	codex.Set("X-Codex-Credits-Balance", "4")
	r, ok = FromCodexHeaders(codex, at(1793491200))
	if !ok {
		t.Fatal("no Codex reading")
	}
	check(t, r, nil, "pro", map[string]meter{
		"primary":   {unit: UnitPercent, used: ptr(30), window: 18000, reset: 1793509200},
		"secondary": {unit: UnitPercent, used: ptr(60), reset: 1793494800},
		"credits":   {unit: UnitCredits, remaining: ptr(4)},
	})
	if _, ok = FromCodexHeaders(http.Header{"Content-Type": {"text/event-stream"}}, at(0)); ok {
		t.Fatal("a response without usage headers yields a reading")
	}
}
