package snapshots

import (
	"context"
	"errors"
	"hash/fnv"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Poll schedule.
const (
	// IdleDelay is how long a credential's requests must pause before it
	// is polled.
	IdleDelay = 10 * time.Second
	// CommandCodeIdleDelay waits for Command Code's counters, which settle
	// about 20 seconds after a call.
	CommandCodeIdleDelay = 30 * time.Second
	// Baseline is the longest a credential with a usage source goes
	// unpolled.
	Baseline = 15 * time.Minute
	// PollTimeout bounds one poll.
	PollTimeout = 15 * time.Second
	tick        = time.Second
)

// Writer stores a snapshot, returning its ID or 0 when it changed nothing.
type Writer interface {
	WriteSnapshot(ctx context.Context, s Snapshot) (int64, error)
}

// PollerOptions configure a Poller.
type PollerOptions struct {
	// Credentials lists the server's current credentials.
	Credentials func() []*cliproxyauth.Auth
	// Client returns the HTTP client a credential's polls use.
	Client func(ctx context.Context, auth *cliproxyauth.Auth) *http.Client
	Writer Writer
	// Fetchers default to the package's Fetchers.
	Fetchers map[string]Fetcher
	Now      func() time.Time
	// Timeout defaults to PollTimeout.
	Timeout time.Duration
}

type credentialState struct {
	lastRequest time.Time
	lastPoll    time.Time
	polling     bool
}

// Poller polls each credential's usage source after its requests pause and
// at least every Baseline, one poll per credential at a time.
type Poller struct {
	opts  PollerOptions
	start time.Time

	mu     sync.Mutex
	states map[string]*credentialState
	stop   context.CancelFunc
	done   chan struct{}
	// remaining holds each credential's latest polled meters for the
	// remaining-usage gauge.
	remaining  map[string]providerMeters
	unregister func()
	wg         sync.WaitGroup
}

type providerMeters struct {
	provider string
	meters   []Meter
}

// NewPoller returns a stopped poller.
func NewPoller(opts PollerOptions) *Poller {
	if opts.Fetchers == nil {
		opts.Fetchers = Fetchers
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout <= 0 {
		opts.Timeout = PollTimeout
	}
	return &Poller{opts: opts, states: map[string]*credentialState{}, remaining: map[string]providerMeters{}}
}

// Touch records a request on a credential.
func (p *Poller) Touch(credentialID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state(credentialID).lastRequest = p.opts.Now()
}

func (p *Poller) state(id string) *credentialState {
	s, ok := p.states[id]
	if !ok {
		s = &credentialState{}
		p.states[id] = s
	}
	return s
}

// baselineOffset staggers credentials' baseline polls across Baseline.
func baselineOffset(id string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(Baseline))
}

func idleDelay(provider string) time.Duration {
	if provider == "commandcode" {
		return CommandCodeIdleDelay
	}
	return IdleDelay
}

// due reports whether a credential needs a poll now.
func (p *Poller) due(auth *cliproxyauth.Auth, s *credentialState, now time.Time) bool {
	if s.polling {
		return false
	}
	if !s.lastRequest.IsZero() && s.lastRequest.After(s.lastPoll) && now.Sub(s.lastRequest) >= idleDelay(auth.Provider) {
		return true
	}
	if s.lastPoll.IsZero() {
		// A credential's first baseline poll comes at its offset after
		// start, so a restart does not poll every credential at once.
		return now.Sub(p.start) >= baselineOffset(auth.ID)
	}
	return now.Sub(s.lastPoll) >= Baseline
}

// Start polls until Stop.
func (p *Poller) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	p.start = p.opts.Now()
	p.stop = cancel
	p.done = make(chan struct{})
	unregister, err := telemetry.RegisterRemainingGauge(p.lowestRemaining)
	if err != nil {
		cancel()
		return err
	}
	p.unregister = unregister
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.PollDue(ctx)
			}
		}
	}()
	return nil
}

// Stop ends polling and waits for polls in progress.
func (p *Poller) Stop() {
	if p.stop == nil {
		return
	}
	p.stop()
	<-p.done
	p.wg.Wait()
	p.unregister()
}

// Pollable reports whether a credential has a usage source the server reads.
func (p *Poller) Pollable(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Disabled {
		return false
	}
	if _, ok := p.opts.Fetchers[auth.Provider]; !ok {
		return false
	}
	return hasToken(auth)
}

// hasToken reports whether a credential holds what its fetcher sends.
func hasToken(auth *cliproxyauth.Auth) bool {
	switch auth.Provider {
	case "kiro", "commandcode":
		return apiKey(auth) != ""
	case "meta":
		return len(token(auth, "dca_token")) > len("dca:")
	case "devin":
		return token(auth, usageTokenAttribute, "session_token", "token") != ""
	case "antigravity":
		return token(auth, accessTokenMetadataKey) != "" && token(auth, "project_id") != ""
	}
	return token(auth, accessTokenMetadataKey) != ""
}

// PollDue starts a poll of each credential that is due, without waiting.
func (p *Poller) PollDue(ctx context.Context) {
	now := p.opts.Now()
	credentials := p.opts.Credentials()
	p.forgetRemoved(credentials)
	for _, auth := range credentials {
		if !p.Pollable(auth) {
			continue
		}
		p.mu.Lock()
		s := p.state(auth.ID)
		run := p.due(auth, s, now)
		if run {
			s.polling = true
		}
		p.mu.Unlock()
		if run {
			p.wg.Add(1)
			go func(auth *cliproxyauth.Auth) {
				defer p.wg.Done()
				p.Poll(ctx, auth)
			}(auth)
		}
	}
}

// Errors of Refresh.
var (
	ErrUnknownCredential = errors.New("no such credential")
	ErrNoUsageSource     = errors.New("the credential has no usage source")
	ErrPollRunning       = errors.New("a poll of the credential is running")
)

// Credentials returns the server's current credentials.
func (p *Poller) Credentials() []*cliproxyauth.Auth { return p.opts.Credentials() }

// Refresh polls one credential now and waits for the poll, returning its
// outcome. It refuses a credential the server does not have, one without a
// usage source, and one whose poll is already running.
func (p *Poller) Refresh(ctx context.Context, credentialID string) (string, error) {
	var auth *cliproxyauth.Auth
	for _, candidate := range p.opts.Credentials() {
		if candidate != nil && candidate.ID == credentialID {
			auth = candidate
			break
		}
	}
	switch {
	case auth == nil:
		return "", ErrUnknownCredential
	case !p.Pollable(auth):
		return "", ErrNoUsageSource
	}
	p.mu.Lock()
	s := p.state(auth.ID)
	if s.polling {
		p.mu.Unlock()
		return "", ErrPollRunning
	}
	s.polling = true
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()
	return p.Poll(ctx, auth), nil
}

// forgetRemoved drops the state and gauge readings of credentials that are
// gone or no longer pollable.
func (p *Poller) forgetRemoved(credentials []*cliproxyauth.Auth) {
	live := make(map[string]bool, len(credentials))
	for _, auth := range credentials {
		if p.Pollable(auth) {
			live[auth.ID] = true
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.remaining {
		if !live[id] {
			delete(p.remaining, id)
		}
	}
	for id, s := range p.states {
		if !live[id] && !s.polling {
			delete(p.states, id)
		}
	}
}

// Poll outcomes.
const (
	OutcomeOK          = "ok"
	OutcomeTimeout     = "timeout"
	OutcomeHTTPError   = "http_error"
	OutcomeUnparseable = "unparseable"
	OutcomeUnchanged   = "unchanged"
)

func outcomeOf(err error) string {
	var httpErr *HTTPError
	var netErr net.Error
	switch {
	case err == nil:
		return OutcomeOK
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return OutcomeTimeout
	case errors.Is(err, ErrUnparseable):
		return OutcomeUnparseable
	case errors.As(err, &httpErr):
		return OutcomeHTTPError
	}
	// A transport failure answered nothing a status could name.
	return OutcomeHTTPError
}

// Poll reads one credential's usage now and stores a snapshot when it
// changed; a failed or unparseable poll stores nothing. It returns the
// poll's outcome.
func (p *Poller) Poll(ctx context.Context, auth *cliproxyauth.Auth) string {
	start := p.opts.Now()
	defer func() {
		p.mu.Lock()
		s := p.state(auth.ID)
		s.polling = false
		s.lastPoll = start
		p.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	ctx, finish := telemetry.StartUsagePoll(ctx, auth.Provider)
	outcome := p.fetchAndStore(ctx, auth, start)
	finish(outcome)
	return outcome
}

func (p *Poller) fetchAndStore(ctx context.Context, auth *cliproxyauth.Auth, observedAt time.Time) string {
	fetcher := p.opts.Fetchers[auth.Provider]
	type result struct {
		reading Reading
		err     error
	}
	done := make(chan result, 1)
	go func() {
		reading, err := fetcher.Fetch(ctx, p.opts.Client(ctx, auth), auth)
		done <- result{reading, err}
	}()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		// A fetcher that ignores its context is abandoned.
		return OutcomeTimeout
	}
	if res.err == nil && ctx.Err() != nil {
		res.err = ctx.Err()
	}
	if res.err != nil {
		return outcomeOf(res.err)
	}
	if res.reading.Empty() {
		return OutcomeUnparseable
	}
	p.mu.Lock()
	p.remaining[auth.ID] = providerMeters{provider: auth.Provider, meters: res.reading.Meters}
	p.mu.Unlock()
	id, err := p.opts.Writer.WriteSnapshot(ctx, Snapshot{Credential: CredentialOf(auth), ObservedAt: observedAt, Source: SourcePoll, Reading: res.reading})
	switch {
	case err != nil:
		// The provider answered, so the outcome is ok; the span's status
		// carries the store's failure without its message, which can hold
		// SQL values.
		trace.SpanFromContext(ctx).SetStatus(codes.Error, "snapshot write failed")
		return OutcomeOK
	case id == 0:
		return OutcomeUnchanged
	}
	return OutcomeOK
}

// lowestRemaining returns each provider's lowest remaining share per meter
// class across its credentials.
func (p *Poller) lowestRemaining() []telemetry.RemainingShare {
	p.mu.Lock()
	defer p.mu.Unlock()
	lowest := map[[2]string]float64{}
	for _, pm := range p.remaining {
		for _, m := range pm.meters {
			share, ok := m.RemainingShare()
			if !ok {
				continue
			}
			key := [2]string{pm.provider, MeterClass(m)}
			if current, seen := lowest[key]; !seen || share < current {
				lowest[key] = share
			}
		}
	}
	out := make([]telemetry.RemainingShare, 0, len(lowest))
	for key, share := range lowest {
		out = append(out, telemetry.RemainingShare{Provider: key[0], Meter: key[1], Share: share})
	}
	return out
}

// Meter classes, the bounded names the remaining-usage gauge reports.
const (
	ClassFiveHour = "five_hour"
	ClassDaily    = "daily"
	ClassWeekly   = "weekly"
	ClassMonthly  = "monthly"
	ClassBalance  = "balance"
	ClassOther    = "other"
)

// MeterClass names a meter by its window: five-hour, daily, weekly, or
// monthly, a balance without a window, or other.
func MeterClass(m Meter) string {
	switch w := m.WindowSeconds; {
	case w > 0 && w <= secondsPerFiveHours:
		return ClassFiveHour
	case w > secondsPerFiveHours && w <= secondsPerDay:
		return ClassDaily
	case w > secondsPerDay && w <= secondsPerWeek:
		return ClassWeekly
	case w > secondsPerWeek:
		return ClassMonthly
	case m.Unit == UnitCredits || m.Unit == UnitUSD:
		return ClassBalance
	}
	return ClassOther
}

// CredentialOf describes a credential for its snapshots.
func CredentialOf(auth *cliproxyauth.Auth) Credential {
	c := Credential{ID: auth.ID, Provider: auth.Provider}
	if auth.Attributes != nil {
		c.Group = auth.Attributes[cliproxyauth.AttributeGroup]
		c.AccountLabel = auth.Attributes[cliproxyauth.AttributeAccountLabel]
		if key := auth.Attributes[usageTokenAttribute]; key != "" {
			c.KeyFingerprint = upstream.Fingerprint(key)
		}
		if auth.Attributes["compat_name"] != "" {
			c.Provider = "openai-compatibility"
		}
	}
	if c.AccountLabel == "" {
		c.AccountLabel = auth.Label
	}
	if auth.Metadata != nil {
		for _, name := range []string{"account_id", "organization_uuid"} {
			if v, ok := auth.Metadata[name].(string); ok && v != "" {
				c.AccountScope = v
				break
			}
		}
	}
	return c
}
