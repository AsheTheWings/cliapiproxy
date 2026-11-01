//go:build component

package generations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const deadline = 5 * time.Second

var errUnavailable = errors.New("store unavailable")

// fakeStore records writes; fail decides each attempt's error, and gate,
// when set, holds every write until it closes or the write is canceled.
type fakeStore struct {
	mu       sync.Mutex
	stored   []uuid.UUID
	attempts map[uuid.UUID]int
	fail     func(id uuid.UUID, attempt int) error
	gate     chan struct{}
	started  chan uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{attempts: map[uuid.UUID]int{}, started: make(chan uuid.UUID, 100)}
}

func (s *fakeStore) Write(ctx context.Context, e *Entry) error {
	id := e.Generation.ID
	s.mu.Lock()
	s.attempts[id]++
	attempt := s.attempts[id]
	gate, fail := s.gate, s.fail
	s.mu.Unlock()
	select {
	case s.started <- id:
	default:
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		if err := fail(id, attempt); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.stored = append(s.stored, id)
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) storedIDs() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.stored...)
}

func generation(body string) capture.Generation {
	return capture.Generation{ID: uuid.New(), Credential: capture.Credential{Provider: "codex"},
		Payloads: []capture.Payload{{Kind: capture.KindClientInput, Body: []byte(body)}}}
}

func newTestWriter(t *testing.T, store Store, queueBytes int64) *Writer {
	t.Helper()
	w, err := NewWriter(store, WriterOptions{PayloadLimit: 1 << 20, QueueBytes: queueBytes,
		Retryable: func(err error) bool { return errors.Is(err, errUnavailable) },
		Backoff:   func(int) time.Duration { return time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(0) })
	return w
}

// points sums an int64 sum metric's points by one attribute.
func points(t *testing.T, rec *telemetrytest.Recorder, name string, key attribute.Key) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, m := range rec.Metrics(t) {
		if m.Name != name {
			continue
		}
		if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
			for _, p := range sum.DataPoints {
				v, _ := p.Attributes.Value(key)
				out[v.AsString()] += p.Value
			}
		}
	}
	return out
}

// outcomes counts write duration observations by outcome.
func outcomes(t *testing.T, rec *telemetrytest.Recorder) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for _, m := range rec.Metrics(t) {
		if h, ok := m.Data.(metricdata.Histogram[float64]); ok && m.Name == "cliapiproxy.store.write.duration" {
			for _, p := range h.DataPoints {
				v, _ := p.Attributes.Value("cliapiproxy.store.write_outcome")
				out[v.AsString()] += p.Count
			}
		}
	}
	return out
}

func dropped(t *testing.T, rec *telemetrytest.Recorder) map[string]int64 {
	return points(t, rec, "cliapiproxy.store.dropped", "cliapiproxy.store.drop_reason")
}

func TestQueueDropsWhatExceedsEitherBound(t *testing.T) {
	rec := telemetrytest.Install(t)
	store := newFakeStore()
	store.gate = make(chan struct{})
	w := newTestWriter(t, store, 1<<20)

	// The writer holds the first generation in a write; the queue then
	// takes MaxQueueEntries more and drops the next.
	w.Submit(generation("first"))
	wait.Receive(t, store.started, deadline, "the writer never started a write")
	for range MaxQueueEntries {
		w.Submit(generation("queued"))
	}
	w.Submit(generation("one too many"))
	if got := w.QueueEntries(); got != MaxQueueEntries+1 {
		t.Fatalf("queue entries = %d, want %d", got, MaxQueueEntries+1)
	}
	if got := dropped(t, rec)["queue_entries"]; got != 1 {
		t.Fatalf("queue_entries drops = %d, want 1", got)
	}
	close(store.gate)
	wait.Until(t, deadline, func() bool { return len(store.storedIDs()) == MaxQueueEntries+1 }, "the queue was not written")
	wait.Until(t, deadline, func() bool { return w.QueueEntries() == 0 && w.QueueBytes() == 0 }, "the queue kept counts after writing")

	// A byte budget smaller than a generation's compressed payloads drops it.
	small := newTestWriter(t, newFakeStore(), 1)
	small.Submit(generation("does not fit"))
	if got := dropped(t, rec)["queue_bytes"]; got != 1 {
		t.Fatalf("queue_bytes drops = %d, want 1", got)
	}
	rec.CheckCatalog(t)
}

func TestUnavailableStoreIsRetriedAndTerminalErrorsDrop(t *testing.T) {
	rec := telemetrytest.Install(t)
	store := newFakeStore()
	retried, broken, next := generation("retried"), generation("broken"), generation("next")
	store.fail = func(id uuid.UUID, attempt int) error {
		switch {
		case id == retried.ID && attempt <= 3:
			return errUnavailable
		case id == broken.ID:
			return errors.New("check constraint violated")
		}
		return nil
	}
	w := newTestWriter(t, store, 1<<20)
	w.Submit(retried)
	w.Submit(broken)
	w.Submit(next)
	wait.Until(t, deadline, func() bool { return len(store.storedIDs()) == 2 }, "the writer stored %v", store.storedIDs())
	if got := store.storedIDs(); got[0] != retried.ID || got[1] != next.ID {
		t.Fatalf("stored %v, want the retried generation, then the next", got)
	}
	store.mu.Lock()
	brokenAttempts := store.attempts[broken.ID]
	store.mu.Unlock()
	if brokenAttempts != terminalAttempts {
		t.Fatalf("the failing generation was tried %d times, want %d", brokenAttempts, terminalAttempts)
	}
	if got := dropped(t, rec)["terminal_write"]; got != 1 {
		t.Fatalf("terminal_write drops = %d, want 1", got)
	}
	if got := outcomes(t, rec); got["ok"] != 2 || got["retryable"] != 3 || got["terminal"] != 3 {
		t.Fatalf("write outcomes = %v, want 2 ok, 3 retryable, 3 terminal", got)
	}
	rec.CheckCatalog(t)
}

func TestShutdownCountsUnwrittenGenerations(t *testing.T) {
	rec := telemetrytest.Install(t)
	store := newFakeStore()
	store.gate = make(chan struct{})
	w := newTestWriter(t, store, 1<<20)
	for range 3 {
		w.Submit(generation("held"))
	}
	wait.Receive(t, store.started, deadline, "the writer never started a write")
	w.Close(10 * time.Millisecond)
	w.Submit(generation("after close"))
	if got := dropped(t, rec)["shutdown"]; got != 4 {
		t.Fatalf("shutdown drops = %d, want the 3 unwritten and 1 late generation", got)
	}
	if n := len(store.storedIDs()); n != 0 {
		t.Fatalf("stored %d generations, want none", n)
	}
	if w.QueueEntries() != 0 || w.QueueBytes() != 0 {
		t.Fatalf("queue = %d entries, %d bytes after shutdown", w.QueueEntries(), w.QueueBytes())
	}
	rec.CheckCatalog(t)
}
