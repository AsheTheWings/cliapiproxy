package snapshots

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var claudeWindow = regexp.MustCompile(`^anthropic-ratelimit-unified-([a-z0-9_]+)-utilization$`)

// claudeHeaderWindows maps a header's window to the meter the OAuth usage
// poll reports for it, with the window's length.
var claudeHeaderWindows = map[string]struct {
	meter   string
	seconds int64
}{
	"5h":        {"five_hour", secondsPerFiveHours},
	"7d":        {"seven_day", secondsPerWeek},
	"7d_opus":   {"seven_day_opus", secondsPerWeek},
	"7d_sonnet": {"seven_day_sonnet", secondsPerWeek},
}

// percentOf turns a fraction into a percent without binary noise such as
// 0.07 becoming 7.000000000000001.
func percentOf(fraction float64) float64 {
	return math.Round(fraction*percentPerFraction*1e6) / 1e6
}

// FromClaudeHeaders reads the anthropic-ratelimit-unified-* headers of a
// Claude response: each window's utilization, as a percent used, and reset,
// under the meter names of the OAuth usage poll.
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
		known, ok := claudeHeaderWindows[window]
		if !ok {
			known.meter = window
		}
		m := Meter{Meter: known.meter, Unit: UnitPercent, Used: ptr(percentOf(utilization)), WindowSeconds: known.seconds}
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
			// A relative reset is pinned to the response; readings compare
			// resets to the minute.
			m.ResetsAt = observedAt.Add(time.Duration(after) * time.Second).Round(time.Minute)
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
