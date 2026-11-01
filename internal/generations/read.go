package generations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
)

// Errors of the store's reads.
var (
	// ErrNotFound marks a generation or payload the store does not hold.
	ErrNotFound = errors.New("not found")
	// ErrInvalidQuery marks a query whose parameters the store refuses.
	ErrInvalidQuery = errors.New("invalid query")
	// ErrRebuild marks a pruned input that could not be rebuilt and
	// checked against its digest.
	ErrRebuild = errors.New("the payload could not be rebuilt")
)

// Limits of the store's reads.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
	// MaxRangeDays bounds the day range of a summary or latency query.
	MaxRangeDays = 400
)

// Reader answers the management API's queries of the store.
type Reader struct {
	pool    *pgxpool.Pool
	decoder *zstd.Decoder
}

// NewReader reads through pool.
func NewReader(pool *pgxpool.Pool) (*Reader, error) {
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	return &Reader{pool: pool, decoder: decoder}, nil
}

// Meter is one meter of a usage snapshot, in its own unit.
type Meter struct {
	Meter         string     `json:"meter"`
	Scope         string     `json:"scope,omitempty"`
	Unit          string     `json:"unit"`
	Used          *float64   `json:"used"`
	Limit         *float64   `json:"limit"`
	Remaining     *float64   `json:"remaining"`
	WindowSeconds *int64     `json:"window_seconds,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
}

// Snapshot is a stored usage snapshot with its meters.
type Snapshot struct {
	ID         int64     `json:"snapshot_id"`
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
	Plan       string    `json:"plan,omitempty"`
	Meters     []Meter   `json:"meters"`
}

// CredentialUsage is a credential with its latest usage snapshot.
type CredentialUsage struct {
	CredentialID  string `json:"credential_id"`
	Provider      string `json:"provider"`
	ProviderGroup string `json:"provider_group,omitempty"`
	AccountLabel  string `json:"account_label,omitempty"`
	// Configured is false for a credential the server no longer has.
	Configured bool `json:"configured"`
	// UsageSource reports a configured credential whose usage the server
	// polls.
	UsageSource bool      `json:"usage_source"`
	Latest      *Snapshot `json:"latest"`
}

// snapshots returns stored snapshots with their meters by ID.
func (r *Reader) snapshots(ctx context.Context, q pgx.Tx, ids []int64) (map[int64]*Snapshot, error) {
	out := map[int64]*Snapshot{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT snapshot_id, observed_at, source, coalesce(plan, '') FROM credential_usage_snapshots
		WHERE snapshot_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		s := &Snapshot{Meters: []Meter{}}
		if err = rows.Scan(&s.ID, &s.ObservedAt, &s.Source, &s.Plan); err != nil {
			rows.Close()
			return nil, err
		}
		out[s.ID] = s
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT snapshot_id, meter, scope, unit, used::float8, "limit"::float8, remaining::float8,
		window_seconds, resets_at FROM credential_usage_meters WHERE snapshot_id = ANY($1) ORDER BY snapshot_id, meter, scope`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var m Meter
		if err = rows.Scan(&id, &m.Meter, &m.Scope, &m.Unit, &m.Used, &m.Limit, &m.Remaining, &m.WindowSeconds, &m.ResetsAt); err != nil {
			return nil, err
		}
		if s := out[id]; s != nil {
			s.Meters = append(s.Meters, m)
		}
	}
	return out, rows.Err()
}

// readOnly runs fn in a read-only transaction that sees one snapshot of
// the store.
func (r *Reader) readOnly(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LatestUsage returns each credential's latest stored snapshot, by
// credential ID; the credentials carry the identity that snapshot names.
func (r *Reader) LatestUsage(ctx context.Context) (map[string]CredentialUsage, error) {
	out := map[string]CredentialUsage{}
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (credential_id) snapshot_id, credential_id, provider,
			coalesce(provider_group, ''), coalesce(account_label, '') FROM credential_usage_snapshots
			ORDER BY credential_id, observed_at DESC, snapshot_id DESC`)
		if err != nil {
			return err
		}
		var ids []int64
		byID := map[int64]string{}
		for rows.Next() {
			var id int64
			var c CredentialUsage
			if err = rows.Scan(&id, &c.CredentialID, &c.Provider, &c.ProviderGroup, &c.AccountLabel); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			byID[id] = c.CredentialID
			out[c.CredentialID] = c
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		snapshots, err := r.snapshots(ctx, tx, ids)
		if err != nil {
			return err
		}
		for id, credential := range byID {
			c := out[credential]
			c.Latest = snapshots[id]
			out[credential] = c
		}
		return nil
	})
	return out, err
}

// Generation is one stored generation's columns. TraceURL links its trace
// in the observability provider; the management API sets it from its trace
// URL template.
type Generation struct {
	ID                   uuid.UUID `json:"generation_id"`
	Time                 time.Time `json:"time"`
	TraceID              string    `json:"trace_id,omitempty"`
	TraceURL             string    `json:"trace_url,omitempty"`
	ConversationID       string    `json:"conversation_id,omitempty"`
	Status               string    `json:"status"`
	ClientKeyLabel       string    `json:"client_key_label,omitempty"`
	ClientKeyFingerprint string    `json:"client_key_fingerprint,omitempty"`
	ClientUserAgent      string    `json:"client_user_agent,omitempty"`
	Provider             string    `json:"provider"`
	ProviderGroup        string    `json:"provider_group,omitempty"`
	AccountLabel         string    `json:"account_label,omitempty"`
	AccountScope         string    `json:"account_scope,omitempty"`
	CredentialID         string    `json:"credential_id,omitempty"`
	KeyFingerprint       string    `json:"key_fingerprint,omitempty"`
	AuthType             string    `json:"auth_type,omitempty"`
	Executor             string    `json:"executor,omitempty"`
	RequestedModel       string    `json:"requested_model,omitempty"`
	UpstreamModel        string    `json:"upstream_model,omitempty"`
	ResponseModel        string    `json:"response_model,omitempty"`
	ReasoningEffort      string    `json:"reasoning_effort,omitempty"`
	RequestedServiceTier string    `json:"requested_service_tier,omitempty"`
	EffectiveServiceTier string    `json:"effective_service_tier,omitempty"`
	ResponseServiceTier  string    `json:"response_service_tier,omitempty"`
	Stream               bool      `json:"stream"`
	Tokens               Tokens    `json:"tokens"`
	TokenQuality         string    `json:"token_quality"`
	TokenSource          string    `json:"token_source"`
	UsageAmount          *float64  `json:"usage_amount,omitempty"`
	UsageUnit            string    `json:"usage_unit,omitempty"`
	DurationMS           *int64    `json:"duration_ms,omitempty"`
	FirstTokenMS         *int64    `json:"first_token_ms,omitempty"`
	SystemInstruction    string    `json:"system_instruction_hash,omitempty"`
	PriorSnapshotID      *int64    `json:"prior_snapshot_id,omitempty"`
	ResponseSnapshotID   *int64    `json:"response_snapshot_id,omitempty"`
}

// Tokens is a token breakdown.
type Tokens struct {
	InputUncached      int64 `json:"input_uncached"`
	InputCacheRead     int64 `json:"input_cache_read"`
	InputCacheWrite    int64 `json:"input_cache_write"`
	OutputNonReasoning int64 `json:"output_non_reasoning"`
	OutputReasoning    int64 `json:"output_reasoning"`
	Unclassified       int64 `json:"unclassified"`
	Total              int64 `json:"total"`
}

const generationColumns = `generation_id, time, coalesce(trace_id, ''), coalesce(conversation_id, ''), status,
	coalesce(client_key_label, ''), coalesce(client_key_fingerprint, ''), coalesce(client_user_agent, ''),
	provider, coalesce(provider_group, ''), coalesce(account_label, ''), coalesce(account_scope, ''),
	coalesce(credential_id, ''), coalesce(key_fingerprint, ''), coalesce(auth_type, ''), coalesce(executor, ''),
	coalesce(requested_model, ''), coalesce(upstream_model, ''), coalesce(response_model, ''), coalesce(reasoning_effort, ''),
	coalesce(requested_service_tier, ''), coalesce(effective_service_tier, ''), coalesce(response_service_tier, ''), stream,
	input_uncached, input_cache_read, input_cache_write, output_non_reasoning, output_reasoning, unclassified, total,
	token_quality, token_source, usage_amount::float8, coalesce(usage_unit, ''), duration_ms, first_token_ms,
	coalesce(system_instruction_hash, ''), prior_snapshot_id, response_snapshot_id`

func scanGeneration(row pgx.Row, extra ...any) (Generation, error) {
	var g Generation
	t := &g.Tokens
	dest := []any{&g.ID, &g.Time, &g.TraceID, &g.ConversationID, &g.Status,
		&g.ClientKeyLabel, &g.ClientKeyFingerprint, &g.ClientUserAgent,
		&g.Provider, &g.ProviderGroup, &g.AccountLabel, &g.AccountScope,
		&g.CredentialID, &g.KeyFingerprint, &g.AuthType, &g.Executor,
		&g.RequestedModel, &g.UpstreamModel, &g.ResponseModel, &g.ReasoningEffort,
		&g.RequestedServiceTier, &g.EffectiveServiceTier, &g.ResponseServiceTier, &g.Stream,
		&t.InputUncached, &t.InputCacheRead, &t.InputCacheWrite, &t.OutputNonReasoning, &t.OutputReasoning, &t.Unclassified, &t.Total,
		&g.TokenQuality, &g.TokenSource, &g.UsageAmount, &g.UsageUnit, &g.DurationMS, &g.FirstTokenMS,
		&g.SystemInstruction, &g.PriorSnapshotID, &g.ResponseSnapshotID}
	return g, row.Scan(append(dest, extra...)...)
}

// GenerationFilter selects generations; empty fields select all.
type GenerationFilter struct {
	// From and To bound the generation's time, From inclusive.
	From, To     time.Time
	Provider     string
	CredentialID string
	Model        string
	Effort       string
	// ClientFingerprint is the client key's fingerprint.
	ClientFingerprint string
	ConversationID    string
	Status            string
	// Limit defaults to DefaultPageSize and is at most MaxPageSize.
	Limit int
	// Cursor continues the listing a page's NextCursor ended.
	Cursor string
}

// GenerationPage is one page of generations, newest first.
type GenerationPage struct {
	Generations []Generation `json:"generations"`
	// NextCursor continues the listing; it is empty on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

func encodeCursor(g Generation) string {
	return base64.RawURLEncoding.EncodeToString([]byte(g.Time.UTC().Format(time.RFC3339Nano) + "|" + g.ID.String()))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("%w: malformed cursor", ErrInvalidQuery)
	}
	at, id, ok := strings.Cut(string(raw), "|")
	t, errTime := time.Parse(time.RFC3339Nano, at)
	u, errID := uuid.Parse(id)
	if !ok || errTime != nil || errID != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("%w: malformed cursor", ErrInvalidQuery)
	}
	return t, u, nil
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Generations lists generations newest first.
func (r *Reader) Generations(ctx context.Context, f GenerationFilter) (GenerationPage, error) {
	limit := f.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return GenerationPage{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidQuery, MaxPageSize)
	}
	if f.Status != "" && f.Status != "completed" && f.Status != "incomplete" {
		return GenerationPage{}, fmt.Errorf("%w: status must be completed or incomplete", ErrInvalidQuery)
	}
	if f.ClientFingerprint != "" && !fingerprintPattern.MatchString(f.ClientFingerprint) {
		return GenerationPage{}, fmt.Errorf("%w: client must be a 12-character key fingerprint", ErrInvalidQuery)
	}
	var where []string
	var args []any
	add := func(condition string, values ...any) {
		for _, v := range values {
			args = append(args, v)
			condition = strings.Replace(condition, "?", "$"+strconv.Itoa(len(args)), 1)
		}
		where = append(where, condition)
	}
	if !f.From.IsZero() {
		add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		add("time < ?", f.To)
	}
	for _, eq := range []struct{ column, value string }{
		{"provider", f.Provider}, {"credential_id", f.CredentialID}, {"upstream_model", f.Model},
		{"reasoning_effort", f.Effort}, {"client_key_fingerprint", f.ClientFingerprint},
		{"conversation_id", f.ConversationID}, {"status", f.Status},
	} {
		if eq.value != "" {
			add(eq.column+" = ?", eq.value)
		}
	}
	if f.Cursor != "" {
		at, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return GenerationPage{}, err
		}
		add("(time, generation_id) < (?, ?)", at, id)
	}
	query := "SELECT " + generationColumns + " FROM generations"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit+1)
	query += " ORDER BY time DESC, generation_id DESC LIMIT $" + strconv.Itoa(len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return GenerationPage{}, err
	}
	defer rows.Close()
	page := GenerationPage{Generations: []Generation{}}
	for rows.Next() {
		g, errScan := scanGeneration(rows)
		if errScan != nil {
			return GenerationPage{}, errScan
		}
		page.Generations = append(page.Generations, g)
	}
	if err = rows.Err(); err != nil {
		return GenerationPage{}, err
	}
	if len(page.Generations) > limit {
		page.Generations = page.Generations[:limit]
		page.NextCursor = encodeCursor(page.Generations[limit-1])
	}
	return page, nil
}

// PayloadInfo describes one stored payload of a generation.
type PayloadInfo struct {
	Kind string `json:"kind"`
	// ContentType is the content type of the body as it was sent.
	ContentType   string `json:"content_type"`
	OriginalBytes int64  `json:"original_bytes"`
	StoredBytes   int64  `json:"stored_bytes"`
	Truncated     bool   `json:"truncated"`
	// Pruned marks an input stored as a residual of a later input.
	Pruned bool `json:"pruned"`
}

// GenerationDetail is one generation with its linked snapshots and the
// kinds and sizes of its payloads.
type GenerationDetail struct {
	Generation
	UsageRaw         json.RawMessage `json:"usage_raw,omitempty"`
	PriorSnapshot    *Snapshot       `json:"prior_snapshot"`
	ResponseSnapshot *Snapshot       `json:"response_snapshot"`
	Payloads         []PayloadInfo   `json:"payloads"`
}

// Generation returns one generation.
func (r *Reader) Generation(ctx context.Context, id uuid.UUID) (GenerationDetail, error) {
	var d GenerationDetail
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		var raw []byte
		g, err := scanGeneration(tx.QueryRow(ctx, "SELECT "+generationColumns+", usage_raw FROM generations WHERE generation_id = $1", id), &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d = GenerationDetail{Generation: g, UsageRaw: raw, Payloads: []PayloadInfo{}}
		var ids []int64
		for _, sid := range []*int64{g.PriorSnapshotID, g.ResponseSnapshotID} {
			if sid != nil {
				ids = append(ids, *sid)
			}
		}
		snapshots, err := r.snapshots(ctx, tx, ids)
		if err != nil {
			return err
		}
		if g.PriorSnapshotID != nil {
			d.PriorSnapshot = snapshots[*g.PriorSnapshotID]
		}
		if g.ResponseSnapshotID != nil {
			d.ResponseSnapshot = snapshots[*g.ResponseSnapshotID]
		}
		rows, err := tx.Query(ctx, `SELECT kind, original_content_type, original_bytes, stored_bytes, truncated, base_payload_id IS NOT NULL
			FROM generation_payloads WHERE generation_id = $1 ORDER BY kind`, id)
		if err != nil {
			return err
		}
		d.Payloads, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PayloadInfo, error) {
			var p PayloadInfo
			return p, row.Scan(&p.Kind, &p.ContentType, &p.OriginalBytes, &p.StoredBytes, &p.Truncated, &p.Pruned)
		})
		return err
	})
	return d, err
}

// PayloadKinds are the kinds of a generation's payloads.
var PayloadKinds = []string{"client_input", "upstream_input", "upstream_response"}

// PayloadBody is one payload's body as the payload route returns it.
type PayloadBody struct {
	Body []byte
	// ContentType is the content type of the body as it was sent.
	ContentType string
	// Rebuilt marks a pruned input rebuilt as the RFC 8785 serialization
	// of the body as sent.
	Rebuilt   bool
	Truncated bool
}

// Payload returns one payload of a generation decompressed, rebuilding a
// pruned input from its base and checking it against its digest.
func (r *Reader) Payload(ctx context.Context, id uuid.UUID, kind string) (PayloadBody, error) {
	if !slices.Contains(PayloadKinds, kind) {
		return PayloadBody{}, fmt.Errorf("%w: kind must be one of %s", ErrInvalidQuery, strings.Join(PayloadKinds, ", "))
	}
	var out PayloadBody
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		var compressed []byte
		var base, restSource int64
		err := tx.QueryRow(ctx, `SELECT body, original_content_type, truncated, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0)
			FROM generation_payloads WHERE generation_id = $1 AND kind = $2`, id, kind).Scan(&compressed, &out.ContentType, &out.Truncated, &base, &restSource)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		body, err := r.decoder.DecodeAll(compressed, nil)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRebuild, err)
		}
		if base == 0 {
			out.Body = body
			return nil
		}
		var baseCompressed []byte
		var baseBase int64
		if err = tx.QueryRow(ctx, `SELECT body, coalesce(base_payload_id, 0) FROM generation_payloads WHERE payload_id = $1`, base).
			Scan(&baseCompressed, &baseBase); err != nil {
			return err
		}
		out.Body, err = r.rebuild(body, baseCompressed, baseBase, base, restSource)
		out.Rebuilt = err == nil
		return err
	})
	if err != nil {
		return PayloadBody{}, err
	}
	return out, nil
}

func (r *Reader) rebuild(residualBody, baseCompressed []byte, baseBase, base, restSource int64) ([]byte, error) {
	if baseBase != 0 || (restSource != 0 && restSource != base) {
		return nil, fmt.Errorf("%w: its base is not a full input", ErrRebuild)
	}
	residual, err := pruning.ParseResidual(residualBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRebuild, err)
	}
	baseBody, err := r.decoder.DecodeAll(baseCompressed, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRebuild, err)
	}
	transcript, err := pruning.Project(baseBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRebuild, err)
	}
	rest := transcript.Rest
	if restSource == 0 {
		// The residual carries its own rest.
		rest = nil
	}
	body, err := pruning.Rebuild(residual, transcript, rest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRebuild, err)
	}
	return body, nil
}

// SummaryDimensions are the dimensions a summary groups by, each with its
// column in generation_usage_daily.
var SummaryDimensions = map[string]string{
	"credential": "credential_id",
	"model":      "upstream_model",
	"effort":     "reasoning_effort",
	"client":     "client_key_fingerprint",
}

// summaryOrder is the order of a bucket's dimension columns.
var summaryOrder = []string{"credential", "model", "effort", "client"}

// SummaryQuery asks for daily buckets over the UTC days From through To.
type SummaryQuery struct {
	From, To time.Time
	GroupBy  []string
}

// Bucket is one day's usage for one combination of the grouped
// dimensions. A dimension the summary was not grouped by is absent, as are
// the identity fields of an ungrouped credential or client.
type Bucket struct {
	Day          string  `json:"day"`
	CredentialID *string `json:"credential_id,omitempty"`
	// Provider, ProviderGroup, and AccountLabel name a grouped credential
	// as its latest generation of the day did.
	Provider          *string  `json:"provider,omitempty"`
	ProviderGroup     *string  `json:"provider_group,omitempty"`
	AccountLabel      *string  `json:"account_label,omitempty"`
	Model             *string  `json:"upstream_model,omitempty"`
	Effort            *string  `json:"reasoning_effort,omitempty"`
	ClientFingerprint *string  `json:"client_key_fingerprint,omitempty"`
	ClientLabel       *string  `json:"client_key_label,omitempty"`
	Generations       int64    `json:"generations"`
	Tokens            Tokens   `json:"tokens"`
	UsageCredits      *float64 `json:"usage_credits,omitempty"`
	UsageUSD          *float64 `json:"usage_usd,omitempty"`
}

// dayRange validates a range of UTC days and returns its first and last
// day.
func dayRange(from, to time.Time) (time.Time, time.Time, error) {
	from, to = utcDay(from), utcDay(to)
	if from.IsZero() || to.IsZero() || to.Before(from) || to.Sub(from) > MaxRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: from and to must be days in order at most %d days apart", ErrInvalidQuery, MaxRangeDays)
	}
	return from, to, nil
}

func utcDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Summary returns daily buckets from generation_usage_daily.
func (r *Reader) Summary(ctx context.Context, q SummaryQuery) ([]Bucket, error) {
	from, to, err := dayRange(q.From, q.To)
	if err != nil {
		return nil, err
	}
	grouped := map[string]bool{}
	for _, dim := range q.GroupBy {
		if _, ok := SummaryDimensions[dim]; !ok {
			return nil, fmt.Errorf("%w: group_by takes credential, model, effort, and client", ErrInvalidQuery)
		}
		grouped[dim] = true
	}
	columns := []string{"day::text"}
	group := []string{"day"}
	for _, dim := range summaryOrder {
		if !grouped[dim] {
			continue
		}
		column := SummaryDimensions[dim]
		columns = append(columns, column)
		group = append(group, column)
		switch dim {
		case "credential":
			for _, identity := range []string{"provider", "provider_group", "account_label"} {
				columns = append(columns, "(array_agg("+identity+" ORDER BY latest_time DESC, account_label))[1]")
			}
		case "client":
			columns = append(columns, "(array_agg(client_key_label ORDER BY latest_time DESC, client_key_label))[1]")
		}
	}
	columns = append(columns, "sum(generations)::bigint", "sum(input_uncached)::bigint", "sum(input_cache_read)::bigint",
		"sum(input_cache_write)::bigint", "sum(output_non_reasoning)::bigint", "sum(output_reasoning)::bigint",
		"sum(unclassified)::bigint", "sum(total)::bigint", "sum(usage_credits)::float8", "sum(usage_usd)::float8")
	keys := strings.Join(group, ", ")
	rows, err := r.pool.Query(ctx, "SELECT "+strings.Join(columns, ", ")+
		" FROM generation_usage_daily WHERE day >= $1 AND day <= $2 GROUP BY "+keys+" ORDER BY "+keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		dest := []any{&b.Day}
		for _, dim := range summaryOrder {
			if !grouped[dim] {
				continue
			}
			switch dim {
			case "credential":
				b.CredentialID, b.Provider, b.ProviderGroup, b.AccountLabel = new(string), new(string), new(string), new(string)
				dest = append(dest, b.CredentialID, b.Provider, b.ProviderGroup, b.AccountLabel)
			case "model":
				b.Model = new(string)
				dest = append(dest, b.Model)
			case "effort":
				b.Effort = new(string)
				dest = append(dest, b.Effort)
			case "client":
				b.ClientFingerprint, b.ClientLabel = new(string), new(string)
				dest = append(dest, b.ClientFingerprint, b.ClientLabel)
			}
		}
		t := &b.Tokens
		dest = append(dest, &b.Generations, &t.InputUncached, &t.InputCacheRead, &t.InputCacheWrite, &t.OutputNonReasoning,
			&t.OutputReasoning, &t.Unclassified, &t.Total, &b.UsageCredits, &b.UsageUSD)
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Latency is one day's time to first token of one upstream model, in
// milliseconds.
type Latency struct {
	Day         string  `json:"day"`
	Model       string  `json:"upstream_model"`
	Generations int64   `json:"generations"`
	P50         float64 `json:"p50_ms"`
	P90         float64 `json:"p90_ms"`
	P99         float64 `json:"p99_ms"`
}

// Latency returns the daily percentiles of time to first token per
// upstream model over the UTC days From through To, from the generations
// that have one.
func (r *Reader) Latency(ctx context.Context, fromDay, toDay time.Time) ([]Latency, error) {
	from, to, err := dayRange(fromDay, toDay)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT ((time AT TIME ZONE 'UTC')::date)::text, coalesce(upstream_model, ''), count(*),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms),
			percentile_cont(0.9) WITHIN GROUP (ORDER BY first_token_ms),
			percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms)
		FROM generations WHERE time >= $1 AND time < $2 AND first_token_ms IS NOT NULL
		GROUP BY 1, 2 ORDER BY 1, 2`, from, to.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Latency, error) {
		var l Latency
		return l, row.Scan(&l.Day, &l.Model, &l.Generations, &l.P50, &l.P90, &l.P99)
	})
}

// TimelinePoint is one meter of one snapshot, with its change since the
// credential's previous snapshot of that meter and what the credential
// served between the two.
type TimelinePoint struct {
	SnapshotID         int64      `json:"snapshot_id"`
	ObservedAt         time.Time  `json:"observed_at"`
	Source             string     `json:"source"`
	Plan               string     `json:"plan,omitempty"`
	Meter              Meter      `json:"meter"`
	PreviousObservedAt *time.Time `json:"previous_observed_at,omitempty"`
	UsedChange         *float64   `json:"used_change,omitempty"`
	RemainingChange    *float64   `json:"remaining_change,omitempty"`
	// Generations and Tokens are absent for the meter's first snapshot.
	Generations *int64  `json:"generations,omitempty"`
	Tokens      *Tokens `json:"tokens,omitempty"`
}

// Timeline returns one credential's snapshot meters observed at or after
// from and before to, oldest first.
func (r *Reader) Timeline(ctx context.Context, credentialID string, from, to time.Time) ([]TimelinePoint, error) {
	if credentialID == "" || from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, fmt.Errorf("%w: a credential and a time range in order are required", ErrInvalidQuery)
	}
	rows, err := r.pool.Query(ctx, `SELECT snapshot_id, observed_at, source, coalesce(plan, ''), meter, scope, unit,
			used::float8, "limit"::float8, remaining::float8, window_seconds, resets_at, previous_observed_at,
			used_change::float8, remaining_change::float8, generations, input_uncached::bigint, input_cache_read::bigint,
			input_cache_write::bigint, output_non_reasoning::bigint, output_reasoning::bigint, unclassified::bigint, total::bigint
		FROM credential_usage_timeline WHERE credential_id = $1 AND observed_at >= $2 AND observed_at < $3
		ORDER BY observed_at, snapshot_id, meter, scope`, credentialID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TimelinePoint, error) {
		var p TimelinePoint
		var t [7]*int64
		m := &p.Meter
		if err := row.Scan(&p.SnapshotID, &p.ObservedAt, &p.Source, &p.Plan, &m.Meter, &m.Scope, &m.Unit,
			&m.Used, &m.Limit, &m.Remaining, &m.WindowSeconds, &m.ResetsAt, &p.PreviousObservedAt,
			&p.UsedChange, &p.RemainingChange, &p.Generations, &t[0], &t[1], &t[2], &t[3], &t[4], &t[5], &t[6]); err != nil {
			return p, err
		}
		if p.Generations != nil {
			p.Tokens = &Tokens{InputUncached: *t[0], InputCacheRead: *t[1], InputCacheWrite: *t[2], OutputNonReasoning: *t[3],
				OutputReasoning: *t[4], Unclassified: *t[5], Total: *t[6]}
		}
		return p, nil
	})
}
