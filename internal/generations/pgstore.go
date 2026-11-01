package generations

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore writes generations to the PostgreSQL store.
type PGStore struct {
	pool *pgxpool.Pool
	// fail, when set, may fail a write at a named stage: "generation"
	// after the generation's insert, "commit" after the commit.
	fail func(stage string) error
}

// NewPGStore writes through pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) failAt(stage string) error {
	if s.fail == nil {
		return nil
	}
	return s.fail(stage)
}

func nullable(s string) any {
	s = text(s)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// text drops what PostgreSQL text refuses, NUL bytes and invalid UTF-8, so
// content a client controls cannot fail a generation's write.
func text(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

func milliseconds(d time.Duration) any {
	if d <= 0 {
		return nil
	}
	return d.Milliseconds()
}

const insertGeneration = `INSERT INTO generations (
	generation_id, time, trace_id, conversation_id, status,
	client_key_label, client_key_fingerprint, client_user_agent,
	provider, provider_group, account_label, account_scope, credential_id, key_fingerprint, auth_type, executor,
	requested_model, upstream_model, response_model, reasoning_effort,
	requested_service_tier, effective_service_tier, response_service_tier, stream,
	input_uncached, input_cache_read, input_cache_write, output_non_reasoning, output_reasoning, unclassified, total,
	token_quality, token_source, usage_raw, usage_amount, usage_unit,
	duration_ms, first_token_ms, system_instruction_hash, response_snapshot_id, prior_snapshot_id
) VALUES (
	$1, $2, $3, $4, $5,
	$6, $7, $8,
	$9, $10, $11, $12, $13, $14, $15, $16,
	$17, $18, $19, $20,
	$21, $22, $23, $24,
	$25, $26, $27, $28, $29, $30, $31,
	$32, $33, $34, $35::numeric, $36,
	$37, $38, $39, $40,
	(SELECT snapshot_id FROM credential_usage_snapshots WHERE credential_id = $13 AND observed_at < $2
		ORDER BY observed_at DESC, snapshot_id DESC LIMIT 1)
) ON CONFLICT (generation_id) DO NOTHING`

const insertPayload = `INSERT INTO generation_payloads (
	generation_id, kind, created_at, conversation_id, body, content_type, original_content_type,
	compression, body_sha256, original_bytes, stored_bytes, truncated
) VALUES ($1, $2, $3, $4, $5, $6, $6, 'zstd', $7, $8, $9, $10)`

// Write stores the entry's system instruction, generation, and payloads in
// one transaction. A generation already stored under the entry's ID, as
// after a commit whose acknowledgement was lost, leaves the store unchanged.
func (s *PGStore) Write(ctx context.Context, e *Entry) error {
	g := e.Generation
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var instruction any
	if g.SystemInstruction != "" {
		sum := instructionHash(g.SystemInstruction)
		hash := hex.EncodeToString(sum[:])
		instruction = hash
		if _, err = tx.Exec(ctx, `INSERT INTO system_instructions (hash, content, first_seen_at) VALUES ($1, $2, $3)
			ON CONFLICT (hash) DO NOTHING`, hash, text(g.SystemInstruction), g.Time); err != nil {
			return err
		}
	}
	var usageRaw any
	// jsonb refuses a \u0000 escape; such a usage object is left out.
	if len(g.UsageRaw) > 0 && !strings.Contains(string(g.UsageRaw), `\u0000`) {
		usageRaw = text(string(g.UsageRaw))
	}
	t := g.Tokens
	// A generation already stored was linked when it was stored; its
	// header snapshot is written only with it.
	var stored bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM generations WHERE generation_id = $1)", g.ID).Scan(&stored); err != nil {
		return err
	}
	if stored {
		return tx.Commit(ctx)
	}
	var responseSnapshot any
	if reading, ok := headerReading(g); ok && g.Credential.ID != "" {
		id, errSnapshot := snapshots.Insert(ctx, tx, snapshots.Snapshot{Credential: snapshotCredential(g.Credential),
			ObservedAt: g.Time.Add(g.Duration), Source: snapshots.SourceResponseHeaders, Reading: reading})
		if errSnapshot != nil {
			return errSnapshot
		}
		if id != 0 {
			responseSnapshot = id
		}
	}
	tag, err := tx.Exec(ctx, insertGeneration,
		g.ID, g.Time, nullable(g.TraceID), nullable(g.ConversationID), g.Status,
		nullable(g.ClientKeyLabel), nullable(g.ClientKeyFingerprint), nullable(g.ClientUserAgent),
		g.Credential.Provider, nullable(g.Credential.Group), nullable(g.Credential.AccountLabel), nullable(g.Credential.AccountScope),
		nullable(g.Credential.ID), nullable(g.Credential.KeyFingerprint), nullable(g.Credential.AuthType), nullable(g.Executor),
		nullable(g.RequestedModel), nullable(g.UpstreamModel), nullable(g.ResponseModel), nullable(g.ReasoningEffort),
		nullable(g.RequestedServiceTier), nullable(g.EffectiveServiceTier), nullable(g.ResponseServiceTier), g.Stream,
		t.Input.UncachedTokens, t.Input.CacheReadTokens, t.Input.CacheWriteTokens, t.Output.NonReasoningTokens, t.Output.ReasoningTokens,
		t.UnclassifiedTokens, t.TotalTokens,
		string(t.Quality), g.TokenSource, usageRaw, nullable(g.UsageAmount), nullable(g.UsageUnit),
		milliseconds(g.Duration), milliseconds(g.FirstToken), instruction, responseSnapshot)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if err = s.failAt("generation"); err != nil {
		return err
	}
	for _, p := range e.Payloads {
		if _, err = tx.Exec(ctx, insertPayload, g.ID, p.Kind, g.Time, nullable(g.ConversationID), p.Compressed, text(p.ContentType),
			hex.EncodeToString(p.SHA256), p.OriginalBytes, int64(len(p.Compressed)), p.Truncated); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.failAt("commit")
}

// headerReading reads a Claude or Codex response's usage headers.
func headerReading(g capture.Generation) (snapshots.Reading, bool) {
	if len(g.QuotaHeaders) == 0 {
		return snapshots.Reading{}, false
	}
	switch g.Credential.Provider {
	case "claude":
		return snapshots.FromClaudeHeaders(g.QuotaHeaders)
	case "codex":
		return snapshots.FromCodexHeaders(g.QuotaHeaders, g.Time.Add(g.Duration))
	}
	return snapshots.Reading{}, false
}

func snapshotCredential(c capture.Credential) snapshots.Credential {
	return snapshots.Credential{ID: c.ID, Provider: c.Provider, Group: c.Group, AccountLabel: c.AccountLabel,
		AccountScope: c.AccountScope, KeyFingerprint: c.KeyFingerprint}
}

// WriteSnapshot stores a polled snapshot and its meters in one transaction
// when its readings changed, returning its ID or 0.
func (s *PGStore) WriteSnapshot(ctx context.Context, snap snapshots.Snapshot) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	id, err := snapshots.Insert(ctx, tx, snap)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// Retryable reports a failure that a later attempt can overcome: a lost or
// refused connection, a timeout, a serialization failure or deadlock, or a
// server that is starting, stopping, or out of connections.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), strings.HasPrefix(pgErr.Code, "57P"), strings.HasPrefix(pgErr.Code, "53"):
			return true
		case pgErr.Code == "40001", pgErr.Code == "40P01":
			return true
		}
		return false
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) || errors.As(err, &netErr) || errors.Is(err, pgx.ErrTxClosed) ||
		errors.Is(err, net.ErrClosed)
}

var _ capture.Sink = (*Writer)(nil)
