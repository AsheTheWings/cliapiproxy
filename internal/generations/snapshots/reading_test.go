//go:build unit

package snapshots

import (
	"testing"
	"time"
)

func TestReadingHashDetectsChangedMeters(t *testing.T) {
	reset := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	monthly := func(used, limit float64, resetsAt time.Time) Reading {
		return Reading{Plan: "pro", Meters: []Meter{
			{Meter: "five_hour", Unit: UnitPercent, Used: ptr(12), WindowSeconds: secondsPerFiveHours},
			{Meter: "monthly", Unit: UnitCredits, Used: ptr(used), Limit: ptr(limit), ResetsAt: resetsAt},
		}}
	}
	base := monthly(40, 100, reset)
	reordered := Reading{Plan: base.Plan, Meters: []Meter{base.Meters[1], base.Meters[0]}, Raw: []byte(`{"other":"raw"}`)}
	if base.Hash() != monthly(40, 100, reset).Hash() || base.Hash() != reordered.Hash() {
		t.Fatal("identical readings hash differently")
	}
	for name, changed := range map[string]Reading{
		"value": monthly(41, 100, reset),
		"limit": monthly(40, 200, reset),
		"reset": monthly(40, 100, reset.Add(time.Hour)),
	} {
		if changed.Hash() == base.Hash() {
			t.Errorf("a changed %s keeps the hash", name)
		}
	}
}
