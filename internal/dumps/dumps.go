// Package dumps finds the store's promoted dumps in a dump directory and
// reports the newest one's age.
package dumps

import (
	"os"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
)

const (
	namePrefix = "cliapiproxy-"
	nameSuffix = ".dump"
	// stampLayout is a promoted dump's UTC start time in its name.
	stampLayout = "20060102T150405Z"
)

// stamp returns the start time a promoted dump's name holds, and whether
// name is exactly cliapiproxy-YYYYMMDDTHHMMSSZ.dump with a valid time.
func stamp(name string) (time.Time, bool) {
	if len(name) != len(namePrefix)+len(stampLayout)+len(nameSuffix) ||
		!strings.HasPrefix(name, namePrefix) || !strings.HasSuffix(name, nameSuffix) {
		return time.Time{}, false
	}
	text := name[len(namePrefix) : len(name)-len(nameSuffix)]
	t, err := time.Parse(stampLayout, text)
	if err != nil || t.Format(stampLayout) != text {
		return time.Time{}, false
	}
	return t, true
}

// Newest returns the start time of the newest promoted dump in dir that is
// not after now, and whether one exists. Files of any other name, and
// entries that are not regular files, are ignored.
func Newest(dir string, now time.Time) (time.Time, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false, err
	}
	var newest time.Time
	found := false
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		t, ok := stamp(e.Name())
		if !ok || t.After(now) {
			continue
		}
		if !found || t.After(newest) {
			newest, found = t, true
		}
	}
	return newest, found, nil
}

// RegisterAgeGauge reports the age of dir's newest promoted dump, read at
// each collection with the time now returns, and returns a function that
// unregisters it. An unreadable directory, like one without a promoted
// dump, reports no value.
func RegisterAgeGauge(dir string, now func() time.Time) (func(), error) {
	return telemetry.RegisterDumpAgeGauge(func() (float64, bool) {
		at := now()
		newest, ok, err := Newest(dir, at)
		if err != nil || !ok {
			return 0, false
		}
		return at.Sub(newest).Seconds(), true
	})
}
