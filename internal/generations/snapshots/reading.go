// Package snapshots reads the plan and usage meters of each credential from
// its provider, polling usage endpoints and parsing response headers, and
// keeps a snapshot in the store whenever the readings change.
package snapshots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Units of a meter.
const (
	UnitCredits  = "credits"
	UnitPercent  = "percent"
	UnitUSD      = "usd"
	UnitRequests = "requests"
)

// Sources of a snapshot.
const (
	SourcePoll            = "poll"
	SourceResponseHeaders = "response_headers"
)

// Meter is one reading in its own unit. A nil value was not reported.
type Meter struct {
	Meter         string
	Scope         string
	Unit          string
	Used          *float64
	Limit         *float64
	Remaining     *float64
	WindowSeconds int64
	ResetsAt      time.Time
}

// Reading is what one poll or response reported about a credential.
type Reading struct {
	Plan   string
	Meters []Meter
	// Raw is the provider's response without secrets, kept for analysis.
	Raw json.RawMessage
}

// Empty reports a reading without a plan or a meter.
func (r Reading) Empty() bool { return r.Plan == "" && len(r.Meters) == 0 }

func number(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

// Hash is the SHA-256 of the normalized plan and meters: identical readings
// hash alike in any meter order, and any changed value, limit, window, or
// reset to the minute changes it. Raw does not take part.
func (r Reading) Hash() string {
	lines := make([]string, 0, len(r.Meters))
	for _, m := range r.Meters {
		reset := ""
		if !m.ResetsAt.IsZero() {
			// Sources report a reset with different precision, so resets
			// compare to the minute.
			reset = strconv.FormatInt(m.ResetsAt.Round(time.Minute).Unix(), 10)
		}
		lines = append(lines, strings.Join([]string{m.Meter, m.Scope, m.Unit, number(m.Used), number(m.Limit), number(m.Remaining),
			strconv.FormatInt(m.WindowSeconds, 10), reset}, "\x1f"))
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(r.Plan + "\x1e" + strings.Join(lines, "\x1e")))
	return hex.EncodeToString(sum[:])
}

// RemainingShare is a meter's remaining fraction of its limit, from 0 to 1,
// when the meter reports enough to tell.
func (m Meter) RemainingShare() (float64, bool) {
	switch {
	case m.Unit == UnitPercent && m.Remaining != nil:
		return clamp(*m.Remaining / 100), true
	case m.Unit == UnitPercent && m.Used != nil:
		return clamp(1 - *m.Used/100), true
	case m.Limit != nil && *m.Limit > 0 && m.Remaining != nil:
		return clamp(*m.Remaining / *m.Limit), true
	case m.Limit != nil && *m.Limit > 0 && m.Used != nil:
		return clamp(1 - *m.Used / *m.Limit), true
	}
	return 0, false
}

func clamp(v float64) float64 { return min(1, max(0, v)) }

func ptr(v float64) *float64 { return &v }
