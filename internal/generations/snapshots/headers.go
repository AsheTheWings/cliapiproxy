package snapshots

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var claudeWindow = regexp.MustCompile(`^anthropic-ratelimit-unified-([a-z0-9_]+)-utilization$`)

// claudeWindowSeconds names the length of Claude's known windows.
var claudeWindowSeconds = map[string]int64{"5h": 5 * 3600, "7d": 7 * 86400}

// FromClaudeHeaders reads the anthropic-ratelimit-unified-* headers of a
// Claude response: each window's utilization, as a percent used, and reset.
func FromClaudeHeaders(h http.Header) (Reading, bool) {
	var r Reading
	for name, values := range h {
		lower := strings.ToLower(name)
		match := claudeWindow.FindStringSubmatch(lower)
		if match == nil || len(values) == 0 {
			continue
		}
		utilization, err := strconv.ParseFloat(strings.TrimSpace(values[0]), 64)
		if err != nil || utilization < 0 {
			continue
		}
		window := match[1]
		m := Meter{Meter: window, Unit: UnitPercent, Used: ptr(utilization * 100), WindowSeconds: claudeWindowSeconds[window]}
		if reset, okReset := epochSeconds(h.Get("anthropic-ratelimit-unified-" + window + "-reset")); okReset {
			m.ResetsAt = reset
		}
		r.Meters = append(r.Meters, m)
	}
	return r, len(r.Meters) > 0
}

// FromCodexHeaders reads the x-codex-* headers of a Codex response: the
// plan, the primary and secondary windows, and the credit balance.
func FromCodexHeaders(h http.Header, observedAt time.Time) (Reading, bool) {
	r := Reading{Plan: strings.TrimSpace(h.Get("X-Codex-Plan-Type"))}
	for _, window := range []string{"Primary", "Secondary"} {
		prefix := "X-Codex-" + window + "-"
		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(prefix+"Used-Percent")), 64)
		if err != nil {
			continue
		}
		m := Meter{Meter: strings.ToLower(window), Unit: UnitPercent, Used: ptr(used)}
		if minutes, errMinutes := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Window-Minutes")), 10, 64); errMinutes == nil && minutes > 0 {
			m.WindowSeconds = minutes * 60
		}
		if reset, ok := epochSeconds(h.Get(prefix + "Reset-At")); ok {
			m.ResetsAt = reset
		} else if after, errAfter := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Reset-After-Seconds")), 10, 64); errAfter == nil && after >= 0 {
			// A relative reset is pinned to the response, so the same
			// window read twice hashes alike only within a second.
			m.ResetsAt = observedAt.Add(time.Duration(after) * time.Second).Truncate(time.Minute)
		}
		r.Meters = append(r.Meters, m)
	}
	if balance, err := strconv.ParseFloat(strings.TrimSpace(h.Get("X-Codex-Credits-Balance")), 64); err == nil {
		r.Meters = append(r.Meters, Meter{Meter: "credits", Unit: UnitCredits, Remaining: ptr(balance)})
	}
	return r, !r.Empty()
}

// epochSeconds parses a Unix time in seconds, or in milliseconds when it is
// too large to be seconds.
func epochSeconds(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	if n > 1e12 {
		return time.UnixMilli(n).UTC(), true
	}
	return time.Unix(n, 0).UTC(), true
}
