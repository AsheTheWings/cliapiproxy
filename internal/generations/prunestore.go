package generations

import (
	"context"
	"errors"
	"fmt"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
)

// pruneLockKey names the advisory lock that keeps pruning passes apart.
const pruneLockKey = "cliapiproxy.prune"

// unconversedBatch bounds one update of inputs without a conversation.
const unconversedBatch = 1000

// needsEvaluation selects full inputs a pass must evaluate.
const needsEvaluation = `base_payload_id IS NULL AND kind IN ('client_input', 'upstream_input')
	AND (prefix_evaluated_at IS NULL OR prefix_evaluation_version < $1)`

// PruneStore is the pruning pass's access to the PostgreSQL store.
type PruneStore struct {
	pool    *pgxpool.Pool
	encoder *zstd.Encoder
	decoder *zstd.Decoder
	// fail, when set, fails the evaluation of a conversation.
	fail func(conversation string) error
	// check verifies a replacement; it is pruning.Check unless a test
	// replaces it.
	check func(pruning.Replacement, map[int64][]byte) error
}

// NewPruneStore prunes through pool.
func NewPruneStore(pool *pgxpool.Pool) (*PruneStore, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	return &PruneStore{pool: pool, encoder: encoder, decoder: decoder, check: pruning.Check}, nil
}

// Lock implements pruning.Store with a session advisory lock held on one
// connection.
func (s *PruneStore) Lock(ctx context.Context) (func(), bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", pruneLockKey).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return func() {
		if _, errUnlock := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtextextended($1, 0))", pruneLockKey); errUnlock != nil {
			// A connection that cannot unlock is closed, which unlocks.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}, true, nil
}

// MarkWithoutConversation implements pruning.Store.
func (s *PruneStore) MarkWithoutConversation(ctx context.Context, version int) error {
	for {
		tag, err := s.pool.Exec(ctx, `UPDATE generation_payloads SET prefix_evaluated_at = now(), prefix_evaluation_version = $1, prefix_failures = 0
			WHERE payload_id IN (SELECT payload_id FROM generation_payloads WHERE conversation_id IS NULL AND `+needsEvaluation+` LIMIT $2)`,
			version, unconversedBatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() < unconversedBatch {
			return nil
		}
	}
}

// Conversations implements pruning.Store.
func (s *PruneStore) Conversations(ctx context.Context, version, limit int, skip []string) ([]string, error) {
	if skip == nil {
		skip = []string{}
	}
	rows, err := s.pool.Query(ctx, `SELECT conversation_id FROM generation_payloads
		WHERE conversation_id IS NOT NULL AND `+needsEvaluation+` AND NOT (conversation_id = ANY($3))
		GROUP BY conversation_id ORDER BY max(created_at), conversation_id LIMIT $2`, version, limit, skip)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s *PruneStore) decompress(body []byte) ([]byte, error) {
	return s.decoder.DecodeAll(body, nil)
}

// Rows implements pruning.Store.
func (s *PruneStore) Rows(ctx context.Context, conversation, kind string) ([]pruning.Row, error) {
	if s.fail != nil {
		if err := s.fail(conversation); err != nil {
			return nil, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT payload_id, body, truncated, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0)
		FROM generation_payloads WHERE conversation_id = $1 AND kind = $2 ORDER BY created_at DESC, payload_id DESC`, conversation, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pruning.Row
	for rows.Next() {
		var r pruning.Row
		var compressed []byte
		if err = rows.Scan(&r.ID, &compressed, &r.Truncated, &r.BaseID, &r.RestSourceID); err != nil {
			return nil, err
		}
		if r.Body, err = s.decompress(compressed); err != nil {
			return nil, fmt.Errorf("payload %d: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// errChanged marks a replacement whose rows changed after it was planned.
var errChanged = errors.New("payloads changed since the pass read them")

type lockedPayload struct {
	body         []byte
	baseID       int64
	restSourceID int64
	storedBytes  int64
	truncated    bool
}

// Apply implements pruning.Store. It locks the rows the replacement
// touches, checks the plan against them, and rewrites them in one
// transaction; a failed check leaves every body as it was.
func (s *PruneStore) Apply(ctx context.Context, rep pruning.Replacement, version int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	ids := []int64{rep.PayloadID, rep.BaseID}
	for _, dep := range rep.Dependents {
		ids = append(ids, dep.PayloadID)
	}
	rows, err := tx.Query(ctx, `SELECT payload_id, body, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0), stored_bytes, truncated
		FROM generation_payloads WHERE payload_id = ANY($1) ORDER BY payload_id FOR UPDATE`, ids)
	if err != nil {
		return 0, err
	}
	locked := map[int64]lockedPayload{}
	for rows.Next() {
		var id int64
		var p lockedPayload
		if err = rows.Scan(&id, &p.body, &p.baseID, &p.restSourceID, &p.storedBytes, &p.truncated); err != nil {
			rows.Close()
			return 0, err
		}
		locked[id] = p
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	payload, base := locked[rep.PayloadID], locked[rep.BaseID]
	if len(locked) != len(ids) || payload.baseID != 0 || payload.truncated || base.baseID != 0 {
		return 0, errChanged
	}
	bodies := map[int64][]byte{}
	for id, p := range locked {
		if bodies[id], err = s.decompress(p.body); err != nil {
			return 0, fmt.Errorf("payload %d: %w", id, err)
		}
	}
	for _, dep := range rep.Dependents {
		if locked[dep.PayloadID].baseID != rep.PayloadID {
			return 0, errChanged
		}
	}
	if err = s.check(rep, bodies); err != nil {
		return 0, err
	}
	var released int64
	rewrite := func(w pruning.Rewrite, evaluated bool) error {
		compressed := s.encoder.EncodeAll(w.Body, nil)
		released += locked[w.PayloadID].storedBytes - int64(len(compressed))
		query := `UPDATE generation_payloads SET body = $2, content_type = $3, stored_bytes = $4, base_payload_id = $5,
			rest_source_payload_id = NULLIF($6, 0)`
		if evaluated {
			query += `, prefix_evaluated_at = now(), prefix_evaluation_version = $7, prefix_failures = 0 WHERE payload_id = $1`
			_, errExec := tx.Exec(ctx, query, w.PayloadID, compressed, pruning.ContentType, len(compressed), w.BaseID, w.RestSourceID, version)
			return errExec
		}
		_, errExec := tx.Exec(ctx, query+` WHERE payload_id = $1`, w.PayloadID, compressed, pruning.ContentType, len(compressed), w.BaseID, w.RestSourceID)
		return errExec
	}
	if err = rewrite(rep.Rewrite, true); err != nil {
		return 0, err
	}
	for _, dep := range rep.Dependents {
		if err = rewrite(dep, false); err != nil {
			return 0, err
		}
	}
	return released, tx.Commit(ctx)
}

// Evaluated implements pruning.Store.
func (s *PruneStore) Evaluated(ctx context.Context, conversation, kind string, ids []int64, version int) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE generation_payloads SET prefix_evaluated_at = now(), prefix_evaluation_version = $4, prefix_failures = 0
		WHERE conversation_id = $1 AND kind = $2 AND payload_id = ANY($3) AND base_payload_id IS NULL`, conversation, kind, ids, version)
	return err
}

// Failed implements pruning.Store. An input evaluated by an older version
// starts counting again from this failure.
func (s *PruneStore) Failed(ctx context.Context, conversation string, version int) (map[string]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var most *int
	if err = tx.QueryRow(ctx, `WITH counted AS (
			UPDATE generation_payloads SET prefix_evaluated_at = NULL,
				prefix_failures = CASE WHEN prefix_evaluated_at IS NULL THEN prefix_failures + 1 ELSE 1 END
			WHERE conversation_id = $2 AND `+needsEvaluation+` RETURNING prefix_failures)
		SELECT max(prefix_failures) FROM counted`, version, conversation).Scan(&most); err != nil {
		return nil, err
	}
	quarantined := map[string]int64{}
	if most != nil && *most >= pruning.QuarantineFailures {
		rows, errQuarantine := tx.Query(ctx, `WITH marked AS (
				UPDATE generation_payloads SET prefix_evaluated_at = now(), prefix_evaluation_version = $1
				WHERE conversation_id = $2 AND `+needsEvaluation+` RETURNING kind)
			SELECT kind, count(*) FROM marked GROUP BY kind`, version, conversation)
		if errQuarantine != nil {
			return nil, errQuarantine
		}
		for rows.Next() {
			var kind string
			var n int64
			if err = rows.Scan(&kind, &n); err != nil {
				rows.Close()
				return nil, err
			}
			quarantined[kind] = n
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	return quarantined, tx.Commit(ctx)
}
