package generations

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	log "github.com/sirupsen/logrus"
)

// sizeInterval is how often the service measures the database's size.
const sizeInterval = time.Minute

// DailyRefreshInterval is how often the service refreshes
// generation_usage_daily.
const DailyRefreshInterval = 15 * time.Minute

// dailyRefreshTimeout bounds one refresh of generation_usage_daily.
const dailyRefreshTimeout = 10 * time.Minute

// Service is the running store: its connection pool, the capture writer,
// the store's gauges, and the reads the management API answers from.
type Service struct {
	*Reader
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
	reader, err := NewReader(pool)
	if err != nil {
		writer.Close(0)
		pool.Close()
		return nil, err
	}
	svc := &Service{Pool: pool, Reader: reader, writer: writer, store: store}
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
	go svc.refreshDaily(sizeCtx)
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

// RefreshDaily refreshes generation_usage_daily without blocking its
// readers.
func RefreshDaily(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY generation_usage_daily")
	return err
}

// refreshDaily refreshes generation_usage_daily at start and every
// DailyRefreshInterval until ctx ends.
func (s *Service) refreshDaily(ctx context.Context) {
	ticker := time.NewTicker(DailyRefreshInterval)
	defer ticker.Stop()
	for {
		qctx, cancel := context.WithTimeout(ctx, dailyRefreshTimeout)
		if err := RefreshDaily(qctx, s.Pool); err != nil && ctx.Err() == nil {
			log.WithError(err).Warn("refreshing generation_usage_daily failed")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Quota returns every credential the server has, with its latest usage
// snapshot, followed by stored credentials the server no longer has.
func (s *Service) Quota(ctx context.Context) ([]CredentialUsage, error) {
	latest, err := s.LatestUsage(ctx)
	if err != nil {
		return nil, err
	}
	out := []CredentialUsage{}
	if s.poller != nil {
		for _, auth := range s.poller.Credentials() {
			if auth == nil {
				continue
			}
			c := snapshots.CredentialOf(auth)
			usage := CredentialUsage{CredentialID: c.ID, Provider: c.Provider, ProviderGroup: c.Group, AccountLabel: c.AccountLabel,
				Configured: true, UsageSource: s.poller.Pollable(auth)}
			if stored, ok := latest[c.ID]; ok {
				usage.Latest = stored.Latest
				delete(latest, c.ID)
			}
			out = append(out, usage)
		}
	}
	for _, stored := range latest {
		out = append(out, stored)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Configured != b.Configured {
			return a.Configured
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.AccountLabel != b.AccountLabel {
			return a.AccountLabel < b.AccountLabel
		}
		return a.CredentialID < b.CredentialID
	})
	return out, nil
}

// ErrNoPoller marks a refresh before usage polling has started.
var ErrNoPoller = errors.New("usage polling has not started")

// RefreshUsage polls one credential's usage source now and returns the
// poll's outcome and the credential's latest usage.
func (s *Service) RefreshUsage(ctx context.Context, credentialID string) (string, CredentialUsage, error) {
	if s.poller == nil {
		return "", CredentialUsage{}, ErrNoPoller
	}
	outcome, err := s.poller.Refresh(ctx, credentialID)
	if err != nil {
		return "", CredentialUsage{}, err
	}
	all, err := s.Quota(ctx)
	if err != nil {
		return outcome, CredentialUsage{}, err
	}
	for _, c := range all {
		if c.CredentialID == credentialID {
			return outcome, c, nil
		}
	}
	return outcome, CredentialUsage{}, snapshots.ErrUnknownCredential
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
