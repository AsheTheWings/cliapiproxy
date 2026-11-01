//go:build component

package dumps_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/dumps"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const dumpAge = "cliapiproxy.store.dump.age"

// observedAge collects the dump age gauge and returns its value and whether
// it reported one.
func observedAge(t *testing.T, rec *telemetrytest.Recorder) (float64, bool) {
	t.Helper()
	for _, m := range rec.Metrics(t) {
		if m.Name != dumpAge {
			continue
		}
		gauge, ok := m.Data.(metricdata.Gauge[float64])
		if !ok {
			t.Fatalf("%s has data of type %T, want a float64 gauge", dumpAge, m.Data)
		}
		if len(gauge.DataPoints) != 1 {
			t.Fatalf("%s has %d data points, want one", dumpAge, len(gauge.DataPoints))
		}
		return gauge.DataPoints[0].Value, true
	}
	return 0, false
}

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dumpName(at time.Time) string {
	return "cliapiproxy-" + at.UTC().Format("20060102T150405Z") + ".dump"
}

func TestDumpAgeGaugeReportsTheNewestPromotedDump(t *testing.T) {
	rec := telemetrytest.Install(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	unregister, err := dumps.RegisterAgeGauge(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	// Neither these files nor a directory with a promoted dump's name count.
	write(t, dir, ".cliapiproxy-dump-20261101T115900Z.tmp")
	write(t, dir, "notes.txt")
	write(t, dir, "cliapiproxy-20261341T250000Z.dump")
	write(t, dir, dumpName(now.Add(time.Hour)))
	if err = os.Mkdir(filepath.Join(dir, dumpName(now.Add(-time.Minute))), 0o700); err != nil {
		t.Fatal(err)
	}
	if age, ok := observedAge(t, rec); ok {
		t.Fatalf("dump age %v without a promoted dump, want no value", age)
	}

	write(t, dir, dumpName(now.Add(-30*time.Hour)))
	stale, ok := observedAge(t, rec)
	if !ok || stale != (30*time.Hour).Seconds() {
		t.Fatalf("dump age %v (reported %v), want the stale dump's %v", stale, ok, (30 * time.Hour).Seconds())
	}

	write(t, dir, dumpName(now.Add(-2*time.Hour)))
	fresh, ok := observedAge(t, rec)
	if !ok || fresh != (2*time.Hour).Seconds() || fresh >= stale {
		t.Fatalf("dump age %v (reported %v), want the fresh dump's %v", fresh, ok, (2 * time.Hour).Seconds())
	}
	rec.CheckCatalog(t)
}

func TestDumpAgeGaugeReportsNothingForAMissingDirectory(t *testing.T) {
	rec := telemetrytest.Install(t)
	unregister, err := dumps.RegisterAgeGauge(filepath.Join(t.TempDir(), "absent"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if age, ok := observedAge(t, rec); ok {
		t.Fatalf("dump age %v for a missing directory, want no value", age)
	}
}
