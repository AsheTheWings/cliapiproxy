package generations

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	"github.com/klauspost/compress/zstd"
)

// Queue limits.
const (
	// MaxQueueEntries is the most generations the write queue holds.
	MaxQueueEntries = 1000
	// terminalAttempts is how often a generation whose write fails with a
	// non-retryable error is tried before it is dropped.
	terminalAttempts = 3
	// ShutdownDrain bounds writing queued generations at shutdown.
	ShutdownDrain = 10 * time.Second
	// writeTimeout bounds one write attempt.
	writeTimeout = 30 * time.Second
	backoffFirst = 100 * time.Millisecond
	backoffMax   = 5 * time.Second
)

// Entry is one generation ready to write, its payloads compressed.
type Entry struct {
	Generation capture.Generation
	Payloads   []StoredPayload
	// Bytes counts the compressed payload bytes the entry holds.
	Bytes int64
}

// StoredPayload is a payload as the store keeps it.
type StoredPayload struct {
	capture.Payload
	Compressed []byte
}

// Store writes one entry atomically. Writing an entry whose generation is
// already stored succeeds without change.
type Store interface {
	Write(ctx context.Context, e *Entry) error
}

// WriterOptions configure a Writer.
type WriterOptions struct {
	PayloadLimit int
	QueueBytes   int64
	// ClientKeyLabel returns a client key's label, or "".
	ClientKeyLabel func(key string) string
	// Retryable classifies a write error; a retryable one is retried
	// without limit while the writer runs.
	Retryable func(error) bool
	// Backoff returns the wait before the n-th retry, counting from 1.
	Backoff func(n int) time.Duration
}

// Writer is the capture sink: a queue bounded in entries and compressed
// bytes that one goroutine drains into the store.
type Writer struct {
	store   Store
	opts    WriterOptions
	queue   chan *Entry
	entries atomic.Int64
	bytes   atomic.Int64
	encoder *zstd.Encoder

	mu     sync.RWMutex
	closed bool
	stop   chan struct{}
	done   chan struct{}
	// abort cancels the write in progress once the shutdown drain ends.
	abort      context.Context
	abortWrite context.CancelFunc
}

// NewWriter starts a writer draining into store.
func NewWriter(store Store, opts WriterOptions) (*Writer, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	if opts.Backoff == nil {
		opts.Backoff = defaultBackoff
	}
	if opts.Retryable == nil {
		opts.Retryable = Retryable
	}
	if opts.ClientKeyLabel == nil {
		opts.ClientKeyLabel = func(string) string { return "" }
	}
	abort, abortWrite := context.WithCancel(context.Background())
	w := &Writer{store: store, opts: opts, queue: make(chan *Entry, MaxQueueEntries), encoder: encoder,
		stop: make(chan struct{}), done: make(chan struct{}), abort: abort, abortWrite: abortWrite}
	go w.run()
	return w, nil
}

func defaultBackoff(n int) time.Duration {
	d := backoffFirst
	for i := 1; i < n && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

// PayloadLimit implements capture.Sink.
func (w *Writer) PayloadLimit() int { return w.opts.PayloadLimit }

// ClientKeyLabel implements capture.Sink.
func (w *Writer) ClientKeyLabel(key string) string { return w.opts.ClientKeyLabel(key) }

// QueueEntries and QueueBytes report what the queue holds.
func (w *Writer) QueueEntries() int64 { return w.entries.Load() }
func (w *Writer) QueueBytes() int64   { return w.bytes.Load() }

// Submit compresses a generation's payloads and queues it, or drops and
// counts it when the queue is full or closed. It never waits for the store.
func (w *Writer) Submit(g capture.Generation) {
	e := &Entry{Generation: g}
	for _, p := range g.Payloads {
		compressed := w.encoder.EncodeAll(p.Body, nil)
		e.Payloads = append(e.Payloads, StoredPayload{Payload: p, Compressed: compressed})
		e.Bytes += int64(len(compressed))
	}
	w.enqueue(e)
}

func (w *Writer) enqueue(e *Entry) {
	ctx := context.Background()
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		telemetry.RecordGenerationDropped(ctx, telemetry.DropShutdown, 1)
		return
	}
	if w.bytes.Add(e.Bytes) > w.opts.QueueBytes {
		w.bytes.Add(-e.Bytes)
		telemetry.RecordGenerationDropped(ctx, telemetry.DropQueueBytes, 1)
		return
	}
	select {
	case w.queue <- e:
		w.entries.Add(1)
	default:
		w.bytes.Add(-e.Bytes)
		telemetry.RecordGenerationDropped(ctx, telemetry.DropQueueEntries, 1)
	}
}

// release removes an entry from the queue's counts once it is written or
// dropped.
func (w *Writer) release(e *Entry) {
	w.entries.Add(-1)
	w.bytes.Add(-e.Bytes)
}

func (w *Writer) run() {
	defer close(w.done)
	for {
		select {
		case e := <-w.queue:
			w.write(e)
		case <-w.stop:
			// Drain what was queued before the queue closed.
			for {
				select {
				case e := <-w.queue:
					w.write(e)
				default:
					return
				}
			}
		}
	}
}

// write stores one entry, retrying retryable failures until the writer
// aborts and dropping it after repeated terminal failures.
func (w *Writer) write(e *Entry) {
	defer w.release(e)
	terminal, retries := 0, 0
	for {
		if w.abort.Err() != nil {
			telemetry.RecordGenerationDropped(context.Background(), telemetry.DropShutdown, 1)
			return
		}
		ctx, cancel := context.WithTimeout(w.abort, writeTimeout)
		start := time.Now()
		err := w.store.Write(ctx, e)
		cancel()
		switch {
		case err == nil:
			telemetry.RecordStoreWrite(context.Background(), telemetry.WriteOK, time.Since(start))
			telemetry.RecordGenerationTokens(context.Background(), e.Generation.Credential.Provider, tokenCounts(e.Generation))
			return
		case w.abort.Err() != nil:
			telemetry.RecordStoreWrite(context.Background(), telemetry.WriteRetryable, time.Since(start))
			telemetry.RecordGenerationDropped(context.Background(), telemetry.DropShutdown, 1)
			return
		case w.opts.Retryable(err):
			telemetry.RecordStoreWrite(context.Background(), telemetry.WriteRetryable, time.Since(start))
			retries++
		default:
			telemetry.RecordStoreWrite(context.Background(), telemetry.WriteTerminal, time.Since(start))
			terminal++
			if terminal >= terminalAttempts {
				telemetry.RecordGenerationDropped(context.Background(), telemetry.DropTerminalWrite, 1)
				return
			}
			retries++
		}
		select {
		case <-time.After(w.opts.Backoff(retries)):
		case <-w.abort.Done():
		}
	}
}

// Close stops accepting generations and writes the queued ones for up to
// drain, then counts those it could not write as dropped.
func (w *Writer) Close(drain time.Duration) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.stop)
	timer := time.NewTimer(drain)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
		w.abortWrite()
		<-w.done
	}
	w.abortWrite()
	w.encoder.Close()
}

func tokenCounts(g capture.Generation) []telemetry.TokenCount {
	t := g.Tokens
	return []telemetry.TokenCount{
		{Type: "input_uncached", Count: t.Input.UncachedTokens},
		{Type: "input_cache_read", Count: t.Input.CacheReadTokens},
		{Type: "input_cache_write", Count: t.Input.CacheWriteTokens},
		{Type: "output_non_reasoning", Count: t.Output.NonReasoningTokens},
		{Type: "output_reasoning", Count: t.Output.ReasoningTokens},
		{Type: "unclassified", Count: t.UnclassifiedTokens},
	}
}

// instructionHash is the key of a system instruction.
func instructionHash(s string) [32]byte { return sha256.Sum256([]byte(s)) }
