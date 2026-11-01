package generations

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sizeInterval is how often the service measures the database's size.
const sizeInterval = time.Minute

// Service is the running store: its connection pool, the capture writer,
// and the store's gauges.
type Service struct {
	Pool       *pgxpool.Pool
	writer     *Writer
	unregister func()
	stopSize   context.CancelFunc
	size       atomic.Int64
}

// Start connects to the store, refuses a schema that does not match this
// build, and installs the capture writer.
func Start(ctx context.Context, s Settings, opts WriterOptions) (*Service, error) {
	pool, err := Open(ctx, s)
	if err != nil {
		return nil, err
	}
	if err = CheckSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	writer, err := NewWriter(NewPGStore(pool), opts)
	if err != nil {
		pool.Close()
		return nil, err
	}
	svc := &Service{Pool: pool, writer: writer}
	svc.size.Store(-1)
	svc.unregister, err = telemetry.RegisterStoreGauges(telemetry.StoreGauges{
		QueueEntries: writer.QueueEntries,
		QueueBytes:   writer.QueueBytes,
		DatabaseSize: func() (int64, bool) { n := svc.size.Load(); return n, n >= 0 },
	})
	if err != nil {
		writer.Close(0)
		pool.Close()
		return nil, err
	}
	sizeCtx, stopSize := context.WithCancel(context.Background())
	svc.stopSize = stopSize
	go svc.measureSize(sizeCtx)
	capture.SetSink(writer)
	return svc, nil
}

func (s *Service) measureSize(ctx context.Context) {
	ticker := time.NewTicker(sizeInterval)
	defer ticker.Stop()
	for {
		var n int64
		qctx, cancel := context.WithTimeout(ctx, writeTimeout)
		if err := s.Pool.QueryRow(qctx, "SELECT pg_database_size(current_database())").Scan(&n); err == nil {
			s.size.Store(n)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Close stops capture, writes queued generations for up to ShutdownDrain,
// and closes the pool.
func (s *Service) Close() {
	capture.SetSink(nil)
	s.writer.Close(ShutdownDrain)
	s.stopSize()
	s.unregister()
	s.Pool.Close()
}
