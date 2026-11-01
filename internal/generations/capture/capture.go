// Package capture collects one generation while the server proxies it: the
// client input, the final upstream attempt's input and response bodies, and
// that attempt's usage record and credential. When the execution completes,
// it hands the generation to the sink the server installed. Without a sink,
// every function here does nothing.
package capture

import (
	"context"
	"crypto/sha256"
	"hash"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/upstream"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

// Payload kinds.
const (
	KindClientInput      = "client_input"
	KindUpstreamInput    = "upstream_input"
	KindUpstreamResponse = "upstream_response"
)

// Statuses of a stored generation.
const (
	StatusCompleted  = "completed"
	StatusIncomplete = "incomplete"
)

// Token sources.
const (
	TokensReported  = "reported"
	TokensEstimated = "estimated"
)

// Sink receives completed generations. Submit must not block on the store.
type Sink interface {
	Submit(Generation)
	// PayloadLimit is the largest body kept of one payload.
	PayloadLimit() int
	// ClientKeyLabel returns the label of a client key, or "".
	ClientKeyLabel(clientKey string) string
}

var sink atomic.Pointer[Sink]

// SetSink installs the sink; nil turns capture off.
func SetSink(s Sink) {
	if s == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&s)
}

func currentSink() Sink {
	if p := sink.Load(); p != nil {
		return *p
	}
	return nil
}

// Credential describes the credential of an upstream attempt without its
// secret: Secrets lists values to remove from the upstream input.
type Credential struct {
	ID             string
	Provider       string
	Group          string
	AccountLabel   string
	AccountScope   string
	KeyFingerprint string
	AuthType       string
	Secrets        []string
}

// Payload is one body of a generation, uncompressed and possibly truncated.
type Payload struct {
	Kind          string
	ContentType   string
	Body          []byte
	OriginalBytes int64
	SHA256        []byte
	Truncated     bool
}

// Generation is one completed generation, ready to store.
type Generation struct {
	ID                   uuid.UUID
	Time                 time.Time
	TraceID              string
	ConversationID       string
	Status               string
	ClientKeyLabel       string
	ClientKeyFingerprint string
	ClientUserAgent      string
	Credential           Credential
	Executor             string
	RequestedModel       string
	UpstreamModel        string
	ResponseModel        string
	ReasoningEffort      string
	RequestedServiceTier string
	EffectiveServiceTier string
	ResponseServiceTier  string
	Stream               bool
	Tokens               usage.TokenBreakdown
	TokenSource          string
	UsageRaw             []byte
	UsageAmount          string
	UsageUnit            string
	Duration             time.Duration
	FirstToken           time.Duration
	SystemInstruction    string
	Payloads             []Payload
}

// body accumulates one payload up to a limit, hashing all of it.
type body struct {
	contentType string
	data        []byte
	total       int64
	digest      hash.Hash
	limit       int
}

func newBody(contentType string, limit int) *body {
	return &body{contentType: contentType, digest: sha256.New(), limit: limit}
}

func (b *body) write(p []byte) {
	b.total += int64(len(p))
	b.digest.Write(p)
	if room := b.limit - len(b.data); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.data = append(b.data, p...)
	}
}

func (b *body) payload(kind string) Payload {
	return Payload{Kind: kind, ContentType: b.contentType, Body: b.data, OriginalBytes: b.total,
		SHA256: b.digest.Sum(nil), Truncated: b.total > int64(len(b.data))}
}

// Capture is the generation one execution builds.
type Capture struct {
	sink      Sink
	start     time.Time
	userAgent string
	stream    bool
	client    *body

	mu       sync.Mutex
	upstream *body
	response *body
	// streamed marks a response of separate chunks, stored one per line.
	streamed bool
	record   *usage.Record
	cred     Credential
	traceID  string
	// ended marks a finished execution and succeeded its outcome; the
	// generation is submitted once it ended successfully and its record
	// is in, whichever comes last.
	ended     bool
	succeeded bool
	submitted bool
}

type contextKey struct{}

// Begin starts capturing an execution whose client sent clientInput. It
// returns ctx unchanged and a nil Capture when capture is off.
func Begin(ctx context.Context, clientInput []byte, contentType, userAgent string, stream bool) (context.Context, *Capture) {
	s := currentSink()
	if s == nil {
		return ctx, nil
	}
	c := &Capture{sink: s, start: time.Now(), userAgent: userAgent, stream: stream, client: newBody(orJSON(contentType), s.PayloadLimit())}
	c.client.write(clientInput)
	return context.WithValue(ctx, contextKey{}, c), c
}

func fromContext(ctx context.Context) *Capture {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(contextKey{}).(*Capture)
	return c
}

func orJSON(contentType string) string {
	if ct := strings.TrimSpace(contentType); ct != "" {
		return ct
	}
	return "application/json"
}

// UpstreamRequest starts a new upstream attempt with the body it sends,
// discarding what an earlier attempt captured.
func UpstreamRequest(ctx context.Context, payload []byte, contentType string) {
	c := fromContext(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.upstream = newBody(orJSON(contentType), c.sink.PayloadLimit())
	c.upstream.write(payload)
	c.response = nil
	c.streamed = false
	c.record = nil
}

// UpstreamWebsocketRequest adds one request frame to the attempt's input; a
// session's frames are stored one per line.
func UpstreamWebsocketRequest(ctx context.Context, payload []byte) {
	c := fromContext(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	if c.upstream == nil || c.upstream.contentType != contentTypeFrames {
		c.upstream = newBody(contentTypeFrames, c.sink.PayloadLimit())
		c.response = nil
		c.record = nil
	}
	writeLine(c.upstream, payload)
}

// contentTypeFrames marks a body of WebSocket frames or stream chunks, one
// per line.
const contentTypeFrames = "application/jsonl"

func writeLine(b *body, p []byte) {
	b.write(p)
	if len(p) == 0 || p[len(p)-1] != '\n' {
		b.write([]byte{'\n'})
	}
}

// UpstreamResponseType records the content type of the attempt's response.
func UpstreamResponseType(ctx context.Context, contentType string) {
	c := fromContext(ctx)
	if c == nil || strings.TrimSpace(contentType) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ended && c.response == nil {
		c.response = newBody(contentType, c.sink.PayloadLimit())
	}
}

// UpstreamResponse appends a chunk of the attempt's response as it arrived.
func UpstreamResponse(ctx context.Context, chunk []byte) {
	c := fromContext(ctx)
	if c == nil || len(chunk) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	if c.response == nil {
		c.response = newBody(orJSON(""), c.sink.PayloadLimit())
	}
	if c.stream {
		c.streamed = true
		writeLine(c.response, chunk)
		return
	}
	c.response.write(chunk)
}

// UpstreamWebsocketResponse appends one response frame to the attempt.
func UpstreamWebsocketResponse(ctx context.Context, payload []byte) {
	c := fromContext(ctx)
	if c == nil || len(payload) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	if c.response == nil || c.response.contentType != contentTypeFrames {
		c.response = newBody(contentTypeFrames, c.sink.PayloadLimit())
	}
	writeLine(c.response, payload)
}

// Attempt records the usage record of the attempt and its credential. ctx
// is the attempt's context, which carries the execution's span.
func Attempt(ctx context.Context, record usage.Record, cred Credential) {
	c := fromContext(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A record published after its stream ended completes the generation;
	// a new attempt cannot follow an end.
	if c.ended && c.record != nil {
		return
	}
	c.record = &record
	c.cred = cred
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		c.traceID = sc.TraceID().String()
	}
	if c.ended {
		c.submitLocked()
	}
}

// Finish ends the execution. The generation is submitted when the execution
// completed, err is nil and the client is still connected, and the final
// attempt's record, which may arrive later, reports success. It submits at
// most once and never blocks on the store.
func (c *Capture) Finish(ctx context.Context, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.ended = true
	c.succeeded = err == nil && ctx.Err() == nil
	c.submitLocked()
}

func (c *Capture) submitLocked() {
	if c.submitted || !c.succeeded || c.record == nil {
		return
	}
	c.submitted = true
	record, cred, traceID := c.record, c.cred, c.traceID
	input, response, streamed := c.upstream, c.response, c.streamed
	if record.Failed || input == nil || response == nil {
		return
	}
	go func() { c.sink.Submit(c.build(record, cred, traceID, input, response, streamed)) }()
}

// build assembles the generation from what the execution captured.
func (c *Capture) build(record *usage.Record, cred Credential, traceID string, input, response *body, streamed bool) Generation {
	id, errID := uuid.NewV7()
	if errID != nil {
		id = uuid.New()
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = c.start
	}
	framed := streamed || response.contentType == contentTypeFrames
	docs := documents(response.data, framed)
	g := Generation{
		ID:                   id,
		Time:                 at.UTC(),
		TraceID:              traceID,
		ConversationID:       strings.TrimSpace(record.SessionID),
		Status:               StatusCompleted,
		ClientUserAgent:      c.userAgent,
		Credential:           cred,
		Executor:             record.ExecutorType,
		RequestedModel:       record.Alias,
		UpstreamModel:        record.Model,
		ResponseModel:        record.ResponseModel,
		ReasoningEffort:      record.ReasoningEffort,
		RequestedServiceTier: record.ServiceTier,
		EffectiveServiceTier: record.EffectiveServiceTier,
		ResponseServiceTier:  record.ResponseServiceTier,
		Stream:               c.stream,
		Tokens:               usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType).TokenBreakdown,
		TokenSource:          TokensReported,
		Duration:             record.Latency,
		FirstToken:           record.TTFT,
		SystemInstruction:    systemInstruction(c.client.data),
	}
	if cred.Provider == "kiro" {
		g.TokenSource = TokensEstimated
	}
	if key := strings.TrimSpace(record.APIKey); key != "" {
		g.ClientKeyFingerprint = upstream.Fingerprint(key)
		g.ClientKeyLabel = c.sink.ClientKeyLabel(key)
	}
	if incomplete(docs) {
		g.Status = StatusIncomplete
	}
	g.UsageRaw, g.UsageAmount, g.UsageUnit = providerUsage(docs)
	redact(input, cred.Secrets)
	g.Payloads = []Payload{c.client.payload(KindClientInput), input.payload(KindUpstreamInput), response.payload(KindUpstreamResponse)}
	return g
}

// redact replaces each secret the server put into the upstream body.
func redact(b *body, secrets []string) {
	if b.total > int64(len(b.data)) {
		// A truncated body keeps the digest of what was sent; only the
		// stored part changes.
		for _, s := range secrets {
			b.data = replaceSecret(b.data, s)
		}
		return
	}
	changed := false
	for _, s := range secrets {
		next := replaceSecret(b.data, s)
		changed = changed || len(next) != len(b.data) || string(next) != string(b.data)
		b.data = next
	}
	if changed {
		b.total = int64(len(b.data))
		b.digest = sha256.New()
		b.digest.Write(b.data)
	}
}
