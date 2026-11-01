package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Credential names the credential a snapshot describes.
type Credential struct {
	ID             string
	Provider       string
	Group          string
	AccountLabel   string
	AccountScope   string
	KeyFingerprint string
}

// Snapshot is a credential's reading at one moment.
type Snapshot struct {
	Credential Credential
	ObservedAt time.Time
	Source     string
	Reading    Reading
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// Insert writes the snapshot and its meters inside tx unless its readings
// hash like the credential's latest snapshot. It returns the new snapshot's
// ID, or 0 when nothing changed. Inserts for one credential take turns, so
// two writers never store the same change twice.
func Insert(ctx context.Context, tx pgx.Tx, s Snapshot) (int64, error) {
	if s.Credential.ID == "" || s.Reading.Empty() {
		return 0, errors.New("a snapshot needs a credential and a reading")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "snapshot:"+s.Credential.ID); err != nil {
		return 0, err
	}
	hash := s.Reading.Hash()
	var latest string
	err := tx.QueryRow(ctx, `SELECT readings_sha256 FROM credential_usage_snapshots WHERE credential_id = $1
		ORDER BY observed_at DESC, snapshot_id DESC LIMIT 1`, s.Credential.ID).Scan(&latest)
	switch {
	case err == nil && latest == hash:
		return 0, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	}
	var raw any
	if len(s.Reading.Raw) > 0 && json.Valid(s.Reading.Raw) {
		raw = string(s.Reading.Raw)
	}
	c := s.Credential
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO credential_usage_snapshots (credential_id, provider, provider_group, account_label, account_scope,
		key_fingerprint, observed_at, source, plan, readings_sha256, raw) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING snapshot_id`,
		c.ID, c.Provider, nullable(c.Group), nullable(c.AccountLabel), nullable(c.AccountScope), nullable(c.KeyFingerprint),
		s.ObservedAt.UTC(), s.Source, nullable(s.Reading.Plan), hash, raw).Scan(&id); err != nil {
		return 0, err
	}
	seen := map[[2]string]bool{}
	for _, m := range s.Reading.Meters {
		key := [2]string{m.Meter, m.Scope}
		if seen[key] {
			continue
		}
		seen[key] = true
		var window any
		if m.WindowSeconds > 0 {
			window = m.WindowSeconds
		}
		if _, err = tx.Exec(ctx, `INSERT INTO credential_usage_meters (snapshot_id, meter, scope, unit, used, "limit", remaining, window_seconds, resets_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, m.Meter, m.Scope, m.Unit, m.Used, m.Limit, m.Remaining, window, nullableTime(m.ResetsAt)); err != nil {
			return 0, err
		}
	}
	return id, nil
}
