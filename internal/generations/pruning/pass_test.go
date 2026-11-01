//go:build component

package pruning

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const deadline = 5 * time.Second

// manualTicker ticks when the test calls tick. The schedule calls C each
// time it waits for a tick, so waits counts how often it has come back.
type manualTicker struct {
	c       chan time.Time
	waits   atomic.Int64
	stopped atomic.Bool
}

func (m *manualTicker) C() <-chan time.Time { m.waits.Add(1); return m.c }
func (m *manualTicker) Stop()               { m.stopped.Store(true) }

// tick delivers one tick and returns once the schedule has handled it and
// waits for the next one.
func (m *manualTicker) tick(t *testing.T) {
	t.Helper()
	wait.Until(t, deadline, func() bool { return m.waits.Load() > 0 }, "the schedule never waited for a tick")
	n := m.waits.Load()
	m.c <- time.Now()
	wait.Until(t, deadline, func() bool { return m.waits.Load() > n }, "the schedule never handled the tick")
}

func TestScheduleRunsOnePassPerTickAndNoneAtZero(t *testing.T) {
	if _, err := StartSchedule(0, func(time.Duration) Ticker { t.Fatal("an interval of 0 made a ticker"); return nil }, func(context.Context) {}); !errors.Is(err, ErrNoInterval) {
		t.Fatalf("err = %v, want ErrNoInterval", err)
	}
	ticker := &manualTicker{c: make(chan time.Time)}
	var interval time.Duration
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	s, err := StartSchedule(time.Hour, func(d time.Duration) Ticker { interval = d; return ticker }, func(context.Context) {
		started <- struct{}{}
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	if interval != time.Hour {
		t.Fatalf("ticker interval = %v, want the pass interval", interval)
	}
	// No pass runs before the first tick, one interval after the start.
	if len(started) != 0 {
		t.Fatal("a pass started before the first tick")
	}
	ticker.tick(t)
	wait.Receive(t, started, deadline, "the first tick started no pass")
	// A tick during the running pass starts none.
	ticker.tick(t)
	release <- struct{}{}
	// Once the pass has ended, a tick starts the next one.
	wait.Until(t, deadline, func() bool {
		ticker.tick(t)
		return len(started) > 0
	}, "a tick after the pass ended started no pass")
	<-started
	close(release)
	s.Stop()
	// Every tick was handled before the release, so any extra pass a tick
	// during a running pass had started would have run by now.
	if len(started) != 0 || !ticker.stopped.Load() {
		t.Fatalf("passes started = %d more, ticker stopped = %v", len(started), ticker.stopped.Load())
	}
}

// fakeStore serves conversations from memory; failing names conversations
// whose evaluation fails, and failPass fails the batch query.
type fakeStore struct {
	mu          sync.Mutex
	rows        map[string][]Row
	failing     map[string]bool
	failPass    bool
	unconversed int
	evaluated   map[string]bool
	failures    map[string]int
}

func (f *fakeStore) Lock(context.Context) (func(), bool, error) { return func() {}, true, nil }
func (f *fakeStore) MarkWithoutConversation(context.Context, int) error {
	f.unconversed++
	return nil
}

func (f *fakeStore) Conversations(_ context.Context, _, limit int, skip []string) ([]string, error) {
	if f.failPass {
		return nil, errors.New("store unavailable")
	}
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var out []string
	for _, c := range []string{"clean", "poison"} {
		if _, ok := f.rows[c]; ok && !skipped[c] && !f.evaluated[c] && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) Rows(_ context.Context, conversation, kind string) ([]Row, error) {
	if f.failing[conversation] {
		return nil, fmt.Errorf("payload 5: %w", ErrMismatch)
	}
	if kind != "upstream_input" {
		return nil, nil
	}
	return f.rows[conversation], nil
}

func (f *fakeStore) Apply(_ context.Context, rep Replacement, _ int) (int64, error) {
	return int64(1000 - len(rep.Body)), nil
}

func (f *fakeStore) Evaluated(_ context.Context, conversation, kind string, _ []int64, _ int) error {
	if kind == "upstream_input" {
		f.evaluated[conversation] = true
	}
	return nil
}

func (f *fakeStore) Failed(_ context.Context, conversation string, _ int) (map[string]int64, error) {
	f.failures[conversation]++
	if f.failures[conversation] >= QuarantineFailures {
		f.evaluated[conversation] = true
		return map[string]int64{"client_input": 2, "upstream_input": 1}, nil
	}
	return nil, nil
}

func newFakeStore() *fakeStore {
	input := func(items string) []byte { return []byte(`{"model":"m","input":[` + items + `]}`) }
	newest := input(`{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}`)
	return &fakeStore{
		rows: map[string][]Row{
			"clean": {{ID: 3, Body: newest},
				{ID: 2, Body: input(`{"role":"user","content":"a"},{"role":"assistant","content":"b"}`)},
				{ID: 1, Body: input(`{"role":"user","content":"a"}`)}},
			"poison": {{ID: 5, Body: newest}},
		},
		failing: map[string]bool{}, evaluated: map[string]bool{}, failures: map[string]int{},
	}
}

// sums adds an int64 counter's points by payload kind.
func sums(t *testing.T, rec *telemetrytest.Recorder, name string) (map[string]int64, string) {
	t.Helper()
	out := map[string]int64{}
	unit := ""
	for _, m := range rec.Metrics(t) {
		if s, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == name {
			unit = m.Unit
			for _, p := range s.DataPoints {
				kind, _ := p.Attributes.Value("cliapiproxy.payload.kind")
				out[kind.AsString()] += p.Value
			}
		}
	}
	return out, unit
}

// outcomes lists each pass span's outcome and error class.
func outcomes(rec *telemetrytest.Recorder) []string {
	var out []string
	for _, span := range rec.Spans("cliapiproxy.store.prune") {
		v, _ := telemetrytest.Attr(span, "cliapiproxy.store.prune_outcome")
		class, _ := telemetrytest.Attr(span, "error.type")
		out = append(out, v.AsString()+"/"+class.AsString())
	}
	return out
}

func TestPassSignals(t *testing.T) {
	rec := telemetrytest.Install(t)
	clean := newFakeStore()
	delete(clean.rows, "poison")
	if res := Run(context.Background(), clean, 50); res.Outcome != OutcomeOK || res.Pruned["upstream_input"] != 2 {
		t.Fatalf("clean pass = %+v", res)
	}
	if clean.unconversed != 1 {
		t.Fatal("the pass did not record inputs without a conversation")
	}
	partial := newFakeStore()
	partial.failing["poison"] = true
	if res := Run(context.Background(), partial, 1); res.Outcome != OutcomePartial || len(res.Failed) != 1 || !partial.evaluated["clean"] {
		t.Fatalf("pass with a failing conversation = %+v; the other conversation must proceed", res)
	}
	failed := newFakeStore()
	failed.failPass = true
	if res := Run(context.Background(), failed, 50); res.Outcome != OutcomeError {
		t.Fatalf("failed pass = %+v", res)
	}

	if got := outcomes(rec); len(got) != 3 || got[0] != "ok/" || got[1] != "partial/residual_mismatch" || got[2] != "error/other" {
		t.Fatalf("span outcomes = %v, want ok, partial from a mismatch, and error", got)
	}
	pruned, unit := sums(t, rec, "cliapiproxy.store.pruned")
	if pruned["upstream_input"] != 4 || pruned["client_input"] != 0 || unit != "{input}" {
		t.Fatalf("pruned = %v in %q, want 4 upstream inputs over two passes", pruned, unit)
	}
	released, unit := sums(t, rec, "cliapiproxy.store.pruned.bytes")
	if released["upstream_input"] <= 0 || unit != "By" {
		t.Fatalf("released = %v in %q", released, unit)
	}
	if _, unit = sums(t, rec, "cliapiproxy.store.quarantined"); unit != "{input}" {
		t.Fatalf("quarantined unit = %q", unit)
	}
	rec.CheckCatalog(t)
}

func TestQuarantineCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	store := newFakeStore()
	store.failing["poison"] = true
	for range QuarantineFailures {
		Run(context.Background(), store, 50)
	}
	if q, _ := sums(t, rec, "cliapiproxy.store.quarantined"); q["client_input"] != 2 || q["upstream_input"] != 1 {
		t.Fatalf("quarantined = %v, want the poison conversation's inputs of both kinds after %d passes", q, QuarantineFailures)
	}
}
