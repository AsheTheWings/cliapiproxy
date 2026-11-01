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
	// Partial marks a reading that reports only some of the credential's
	// meters, as response headers do: the plan and meters it lacks carry
	// over from the credential's latest snapshot, so every snapshot holds
	// the credential's whole known state.
	Partial bool
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
	var latest string
	var latestID int64
	var latestPlan *string
	err := tx.QueryRow(ctx, `SELECT snapshot_id, readings_sha256, plan FROM credential_usage_snapshots WHERE credential_id = $1
		ORDER BY observed_at DESC, snapshot_id DESC LIMIT 1`, s.Credential.ID).Scan(&latestID, &latest, &latestPlan)
	switch {
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	case err == nil && s.Partial:
		previous, errMeters := meters(ctx, tx, latestID)
		if errMeters != nil {
			return 0, errMeters
		}
		plan := ""
		if latestPlan != nil {
			plan = *latestPlan
		}
		s.Reading = merge(Reading{Plan: plan, Meters: previous}, s.Reading)
	}
	hash := s.Reading.Hash()
	if err == nil && latest == hash {
		return 0, nil
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

// meters reads a snapshot's meters.
func meters(ctx context.Context, tx pgx.Tx, snapshotID int64) ([]Meter, error) {
	rows, err := tx.Query(ctx, `SELECT meter, scope, unit, used::float8, "limit"::float8, remaining::float8, coalesce(window_seconds, 0), resets_at
		FROM credential_usage_meters WHERE snapshot_id = $1`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meter
	for rows.Next() {
		var m Meter
		var resets *time.Time
		if err = rows.Scan(&m.Meter, &m.Scope, &m.Unit, &m.Used, &m.Limit, &m.Remaining, &m.WindowSeconds, &resets); err != nil {
			return nil, err
		}
		if resets != nil {
			m.ResetsAt = resets.UTC()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// merge overlays a partial reading on the previous one: its plan, when it
// has one, and its meters replace the previous plan and the meters of the
// same name and scope.
func merge(previous, partial Reading) Reading {
	out := Reading{Plan: previous.Plan, Raw: partial.Raw}
	if partial.Plan != "" {
		out.Plan = partial.Plan
	}
	replaced := map[[2]string]bool{}
	for _, m := range partial.Meters {
		replaced[[2]string{m.Meter, m.Scope}] = true
	}
	for _, m := range previous.Meters {
		if !replaced[[2]string{m.Meter, m.Scope}] {
			out.Meters = append(out.Meters, m)
		}
	}
	out.Meters = append(out.Meters, partial.Meters...)
	return out
}
