//go:build integration

package generations

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// usageRow is one generation the summary tests store.
type usageRow struct {
	at                  string
	credential, label   string
	client, clientLabel string
	model               string
	input, output       int64
	firstTokenMS        int64 // 0 stores no time to first token
	provider, group     string
}

func storeUsageRows(t *testing.T, store *PGStore, rows []usageRow) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for _, r := range rows {
		at, err := time.Parse(time.RFC3339, r.at)
		if err != nil {
			t.Fatal(err)
		}
		g := capture.Generation{ID: uuid.New(), Time: at, Status: capture.StatusCompleted, ClientKeyLabel: r.clientLabel,
			ClientKeyFingerprint: r.client, UpstreamModel: r.model, ReasoningEffort: "high", TokenSource: capture.TokensReported,
			Credential: capture.Credential{ID: r.credential, Provider: r.provider, Group: r.group, AccountLabel: r.label},
			Tokens: usage.TokenBreakdown{Quality: "complete", TotalTokens: r.input + r.output,
				Input: usage.TokenInputBreakdown{UncachedTokens: r.input}, Output: usage.TokenOutputBreakdown{NonReasoningTokens: r.output}},
			FirstToken: time.Duration(r.firstTokenMS) * time.Millisecond, Duration: time.Second}
		if err = store.Write(context.Background(), &Entry{Generation: g}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, g.ID)
	}
	return ids
}

// percentileCont is PostgreSQL's percentile_cont over values.
func percentileCont(values []float64, p float64) float64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	position := p * float64(len(sorted)-1)
	lower := math.Floor(position)
	upper := math.Ceil(position)
	return sorted[int(lower)] + (position-lower)*(sorted[int(upper)]-sorted[int(lower)])
}

var summaryRows = []usageRow{
	{"2026-10-01T10:00:00Z", "cred-a", "alpha", "aaaaaaaaaaaa", "laptop", "m1", 100, 10, 100, "codex", ""},
	// cred-a's label changes within the day; the later one names it.
	{"2026-10-01T11:00:00Z", "cred-a", "alpha renamed", "bbbbbbbbbbbb", "server", "m1", 200, 20, 300, "codex", ""},
	{"2026-10-01T12:00:00Z", "cred-b", "beta", "aaaaaaaaaaaa", "laptop", "m2", 50, 5, 0, "claude", "team"},
	{"2026-10-01T13:00:00Z", "cred-b", "beta", "aaaaaaaaaaaa", "laptop", "m1", 70, 7, 250, "claude", "team"},
	{"2026-10-01T14:00:00Z", "cred-b", "beta", "aaaaaaaaaaaa", "laptop", "m1", 30, 3, 120, "claude", "team"},
	{"2026-10-02T09:00:00Z", "cred-a", "alpha renamed", "aaaaaaaaaaaa", "laptop", "m1", 10, 1, 50, "codex", ""},
	{"2026-10-02T23:59:59Z", "cred-b", "beta", "bbbbbbbbbbbb", "server", "m2", 5, 2, 1000, "claude", "team"},
	// Outside the queried days.
	{"2026-10-03T00:00:00Z", "cred-a", "alpha renamed", "aaaaaaaaaaaa", "laptop", "m1", 999, 9, 9, "codex", ""},
}

func summaryDay(t *testing.T, day string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSummaryAndLatencyMatchTheStoredGenerations(t *testing.T) {
	pool := migratedPool(t)
	storeUsageRows(t, NewPGStore(pool), summaryRows)
	ctx := context.Background()
	if err := RefreshDaily(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(pool)
	if err != nil {
		t.Fatal(err)
	}
	from, to := summaryDay(t, "2026-10-01"), summaryDay(t, "2026-10-02")
	inRange := summaryRows[:len(summaryRows)-1]

	// Client-by-account cells hold the generations and tokens of each pair.
	type cell struct{ day, credential, client string }
	type sums struct{ generations, input, output, total int64 }
	want := map[cell]sums{}
	for _, r := range inRange {
		k := cell{r.at[:10], r.credential, r.client}
		s := want[k]
		s.generations++
		s.input += r.input
		s.output += r.output
		s.total += r.input + r.output
		want[k] = s
	}
	buckets, err := reader.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: []string{"credential", "client"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != len(want) {
		t.Fatalf("%d client-by-account buckets, want %d: %+v", len(buckets), len(want), buckets)
	}
	for _, b := range buckets {
		k := cell{b.Day, *b.CredentialID, *b.ClientFingerprint}
		got := sums{b.Generations, b.Tokens.InputUncached, b.Tokens.OutputNonReasoning, b.Tokens.Total}
		if got != want[k] {
			t.Fatalf("bucket %+v sums %+v, want %+v", k, got, want[k])
		}
		if b.Model != nil || b.Effort != nil {
			t.Fatalf("bucket %+v carries a dimension it was not grouped by", k)
		}
		// Each cell is named by its own latest generation.
		if k == (cell{"2026-10-01", "cred-a", "bbbbbbbbbbbb"}) && *b.AccountLabel != "alpha renamed" {
			t.Fatalf("cred-a's server cell on 2026-10-01 is labeled %q", *b.AccountLabel)
		}
		if *b.CredentialID == "cred-b" && (*b.Provider != "claude" || *b.ProviderGroup != "team" || *b.AccountLabel != "beta") {
			t.Fatalf("cred-b bucket identity = %s %s %s", *b.Provider, *b.ProviderGroup, *b.AccountLabel)
		}
		wantLabel := map[string]string{"aaaaaaaaaaaa": "laptop", "bbbbbbbbbbbb": "server"}[k.client]
		if *b.ClientLabel != wantLabel {
			t.Fatalf("bucket %+v client label %q, want %q", k, *b.ClientLabel, wantLabel)
		}
	}

	// Grouped by credential alone, the day's later label names cred-a.
	buckets, err = reader.Summary(ctx, SummaryQuery{From: from, To: from, GroupBy: []string{"credential"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range buckets {
		if *b.CredentialID == "cred-a" {
			found = true
			if *b.AccountLabel != "alpha renamed" || b.Tokens.Total != 330 {
				t.Fatalf("cred-a's day = %+v, want the later label and 330 tokens", b)
			}
		}
	}
	if !found {
		t.Fatal("no bucket for cred-a on 2026-10-01")
	}

	// A model-only bucket spans both accounts and names neither.
	buckets, err = reader.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: []string{"model"}})
	if err != nil {
		t.Fatal(err)
	}
	var m1 *Bucket
	for i := range buckets {
		if buckets[i].Day == "2026-10-01" && *buckets[i].Model == "m1" {
			m1 = &buckets[i]
		}
	}
	if m1 == nil || m1.Generations != 4 || m1.Tokens.Total != 440 {
		t.Fatalf("m1 on 2026-10-01 = %+v, want 4 generations and 440 tokens across both accounts", m1)
	}
	if m1.CredentialID != nil || m1.Provider != nil || m1.AccountLabel != nil || m1.ClientFingerprint != nil || m1.ClientLabel != nil {
		t.Fatalf("a model-only bucket carries identity fields: %+v", m1)
	}

	// Latency percentiles equal percentile_cont over the rows that have a
	// time to first token.
	type dayModel struct{ day, model string }
	firstTokens := map[dayModel][]float64{}
	for _, r := range inRange {
		if r.firstTokenMS > 0 {
			k := dayModel{r.at[:10], r.model}
			firstTokens[k] = append(firstTokens[k], float64(r.firstTokenMS))
		}
	}
	latency, err := reader.Latency(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(latency) != len(firstTokens) {
		t.Fatalf("%d latency rows, want %d: %+v", len(latency), len(firstTokens), latency)
	}
	for _, l := range latency {
		values := firstTokens[dayModel{l.Day, l.Model}]
		if int(l.Generations) != len(values) {
			t.Fatalf("latency %s %s counts %d, want %d", l.Day, l.Model, l.Generations, len(values))
		}
		for _, q := range []struct {
			p   float64
			got float64
		}{{0.5, l.P50}, {0.9, l.P90}, {0.99, l.P99}} {
			if want := percentileCont(values, q.p); math.Abs(q.got-want) > 1e-9 {
				t.Fatalf("latency %s %s p%v = %v, want %v", l.Day, l.Model, q.p*100, q.got, want)
			}
		}
	}

	if _, err = reader.Summary(ctx, SummaryQuery{From: to, To: from}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("days out of order: err = %v", err)
	}
	if _, err = reader.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: []string{"provider"}}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("unknown dimension: err = %v", err)
	}
}

func TestGenerationsListFiltersAndPages(t *testing.T) {
	pool := migratedPool(t)
	ids := storeUsageRows(t, NewPGStore(pool), summaryRows)
	reader, err := NewReader(pool)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var seen []uuid.UUID
	f := GenerationFilter{Provider: "claude", Limit: 2}
	for {
		page, errPage := reader.Generations(ctx, f)
		if errPage != nil {
			t.Fatal(errPage)
		}
		for _, g := range page.Generations {
			seen = append(seen, g.ID)
		}
		if page.NextCursor == "" {
			break
		}
		f.Cursor = page.NextCursor
	}
	// claude's generations, newest first.
	want := []uuid.UUID{ids[6], ids[4], ids[3], ids[2]}
	if !slices.Equal(seen, want) {
		t.Fatalf("paged claude generations = %v, want %v", seen, want)
	}
	page, err := reader.Generations(ctx, GenerationFilter{ClientFingerprint: "bbbbbbbbbbbb", Model: "m1",
		From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(page.Generations) != 1 || page.Generations[0].ID != ids[1] {
		t.Fatalf("filtered page = %+v, %v", page, err)
	}
	detail, err := reader.Generation(ctx, ids[1])
	if err != nil || detail.AccountLabel != "alpha renamed" || detail.Tokens.Total != 220 || detail.FirstTokenMS == nil || *detail.FirstTokenMS != 300 {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if _, err = reader.Generation(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown generation: err = %v", err)
	}
	if _, err = reader.Generations(ctx, GenerationFilter{Cursor: "not a cursor"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("malformed cursor: err = %v", err)
	}
}

func TestPayloadsAreReturnedAsSentOrRebuilt(t *testing.T) {
	h := newPruneHarness(t)
	for _, items := range []int{1, 3, 5} {
		h.write(prunedTurn{conversation: "chat", items: items, rest: `"instructions":"be brief",`})
	}
	if res := h.pass(); res.Outcome != pruning.OutcomeOK {
		t.Fatalf("pass = %+v", res)
	}
	reader, err := NewReader(h.pool)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rows, err := h.pool.Query(ctx, `SELECT generation_id, kind, payload_id, base_payload_id IS NOT NULL FROM generation_payloads ORDER BY payload_id`)
	if err != nil {
		t.Fatal(err)
	}
	type stored struct {
		generation uuid.UUID
		kind       string
		id         int64
		pruned     bool
	}
	payloads, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
		var s stored
		return s, row.Scan(&s.generation, &s.kind, &s.id, &s.pruned)
	})
	if err != nil {
		t.Fatal(err)
	}
	var residual stored
	pruned := 0
	for _, p := range payloads {
		body, err := reader.Payload(ctx, p.generation, p.kind)
		if err != nil {
			t.Fatalf("payload %d: %v", p.id, err)
		}
		if p.kind == capture.KindUpstreamResponse {
			if !bytes.Equal(body.Body, []byte(`{"ok":true}`)) || body.Rebuilt {
				t.Fatalf("response %d = %s", p.id, body.Body)
			}
			continue
		}
		sent := h.sent[p.id]
		want := sent.body
		if p.pruned {
			pruned++
			residual = p
			original, errParse := pruning.Parse(sent.body)
			if errParse != nil {
				t.Fatal(errParse)
			}
			want = original.Canonical()
		}
		if !bytes.Equal(body.Body, want) || body.Rebuilt != p.pruned || body.ContentType != sent.contentType {
			t.Fatalf("payload %d = %s (%s, rebuilt %v), want %s (%s, rebuilt %v)", p.id, body.Body, body.ContentType, body.Rebuilt,
				want, sent.contentType, p.pruned)
		}
	}
	if pruned != 4 {
		t.Fatalf("%d pruned inputs, want 4", pruned)
	}

	// A residual whose digest no longer matches answers an error.
	tampered := h.encoder.EncodeAll([]byte(`{"version":1,"projection":"responses.input","items":1,"canonical_sha256":"`+
		"0000000000000000000000000000000000000000000000000000000000000000"+`"}`), nil)
	if residual.kind != capture.KindClientInput {
		var other stored
		for _, p := range payloads {
			if p.pruned && p.kind == capture.KindClientInput {
				other = p
			}
		}
		residual = other
	}
	if _, err = h.pool.Exec(ctx, `UPDATE generation_payloads SET body = $1 WHERE payload_id = $2`, tampered, residual.id); err != nil {
		t.Fatal(err)
	}
	if body, err := reader.Payload(ctx, residual.generation, residual.kind); !errors.Is(err, ErrRebuild) || body.Body != nil {
		t.Fatalf("tampered residual = %q, %v; want ErrRebuild and no body", body.Body, err)
	}
	if _, err = reader.Payload(ctx, uuid.New(), capture.KindClientInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown generation: err = %v", err)
	}
	if _, err = reader.Payload(ctx, residual.generation, "headers"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("unknown kind: err = %v", err)
	}
}

func TestQuotaAndTimelineReadSnapshots(t *testing.T) {
	pool := migratedPool(t)
	store := NewPGStore(pool)
	ctx := context.Background()
	credential := snapshots.Credential{ID: "kiro-1", Provider: "kiro", AccountLabel: "work"}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	used := func(v float64) *float64 { return &v }
	for i, value := range []float64{10, 25} {
		if _, err := store.WriteSnapshot(ctx, snapshots.Snapshot{Credential: credential, ObservedAt: start.Add(time.Duration(i) * time.Hour),
			Source: snapshots.SourcePoll, Reading: snapshots.Reading{Plan: "pro", Meters: []snapshots.Meter{
				{Meter: "monthly", Unit: snapshots.UnitCredits, Used: used(value), Limit: used(100)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	storeUsageRows(t, store, []usageRow{{"2026-10-01T00:30:00Z", "kiro-1", "work", "aaaaaaaaaaaa", "laptop", "m1", 40, 2, 0, "kiro", ""}})
	reader, err := NewReader(pool)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reader.LatestUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := latest["kiro-1"]
	if got.Latest == nil || got.AccountLabel != "work" || len(got.Latest.Meters) != 1 || *got.Latest.Meters[0].Used != 25 {
		t.Fatalf("latest usage = %+v", got)
	}
	points, err := reader.Timeline(ctx, "kiro-1", start, start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Generations != nil || points[1].UsedChange == nil || *points[1].UsedChange != 15 ||
		points[1].Generations == nil || *points[1].Generations != 1 || points[1].Tokens.Total != 42 {
		t.Fatalf("timeline = %+v", points)
	}
	if _, err = reader.Timeline(ctx, "", start, start); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("timeline without a credential: err = %v", err)
	}
}
