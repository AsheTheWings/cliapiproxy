package generations

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sizeInterval is how often the service measures the database's size.
const sizeInterval = time.Minute

// Service is the running store: its connection pool, the capture writer,
// and the store's gauges.
type Service struct {
	Pool       *pgxpool.Pool
	writer     *Writer
	store      *PGStore
	poller     *snapshots.Poller
	pruning    *pruning.Schedule
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
	store := NewPGStore(pool)
	writer, err := NewWriter(store, opts)
	if err != nil {
		pool.Close()
		return nil, err
	}
	svc := &Service{Pool: pool, writer: writer, store: store}
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

// StartPolling polls the usage of the credentials list returns, through the
// HTTP clients client builds, until Close.
func (s *Service) StartPolling(list func() []*cliproxyauth.Auth, client func(context.Context, *cliproxyauth.Auth) *http.Client) error {
	s.poller = snapshots.NewPoller(snapshots.PollerOptions{Credentials: list, Client: client, Writer: s.store})
	if err := s.poller.Start(); err != nil {
		s.poller = nil
		return err
	}
	capture.SetActivity(s.poller.Touch)
	return nil
}

// StartPruning runs a pruning pass every interval, evaluating batch
// conversations at a time, until Close; an interval of 0 runs none.
func (s *Service) StartPruning(interval time.Duration, batch int) error {
	if interval <= 0 {
		return nil
	}
	store, err := NewPruneStore(s.Pool)
	if err != nil {
		return err
	}
	s.pruning, err = pruning.StartSchedule(interval, pruning.NewTimeTicker, func(ctx context.Context) {
		pruning.Run(ctx, store, batch)
	})
	return err
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
	s.pruning.Stop()
	capture.SetSink(nil)
	if s.poller != nil {
		capture.SetActivity(nil)
		s.poller.Stop()
	}
	s.writer.Close(ShutdownDrain)
	s.stopSize()
	s.unregister()
	s.Pool.Close()
}
