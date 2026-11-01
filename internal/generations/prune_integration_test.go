//go:build integration

package generations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/pruning"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
)

const (
	clientContentType   = "application/json"
	upstreamContentType = "application/json; charset=utf-8"
)

// pruneHarness stores conversations turn by turn and runs pruning passes
// over them.
type pruneHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	pg      *PGStore
	prune   *PruneStore
	encoder *zstd.Encoder
	decoder *zstd.Decoder
	mu      sync.Mutex
	clock   time.Time
	// sent holds each stored input as it was sent, by payload ID.
	sent map[int64]sentInput
}

type sentInput struct {
	body        []byte
	contentType string
	sha256      string
	bytes       int64
}

// prunedTurn is one generation of a conversation: items messages, the
// request fields outside the item list in rest, and a client input cut
// short when truncated.
type prunedTurn struct {
	conversation string
	items        int
	rest         string
	truncated    bool
}

func newPruneHarness(t *testing.T) *pruneHarness {
	t.Helper()
	pool := migratedPool(t)
	prune, err := NewPruneStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(decoder.Close)
	return &pruneHarness{t: t, pool: pool, pg: NewPGStore(pool), prune: prune, encoder: encoder, decoder: decoder,
		clock: time.Now().UTC().Truncate(time.Millisecond), sent: map[int64]sentInput{}}
}

// turnItems renders a conversation's first n messages. Assistant messages
// put their keys out of order and add spaces, so their canonical form
// differs from what was sent.
func turnItems(n int) string {
	items := make([]string, n)
	for i := range items {
		if i%2 == 0 {
			items[i] = fmt.Sprintf(`{"role":"user","content":"question %d"}`, i)
		} else {
			items[i] = fmt.Sprintf(`{ "content" : "answer %d", "role":"assistant" }`, i)
		}
	}
	return strings.Join(items, ",")
}

// store writes a turn as one generation with both inputs and a response.
func (h *pruneHarness) store(turn prunedTurn) error {
	client := []byte(`{"model":"m",` + turn.rest + `"input":[` + turnItems(turn.items) + `]}`)
	upstream := []byte(`{"messages":[` + turnItems(turn.items) + `],` + turn.rest + `"max_tokens":100,"model":"m"}`)
	payload := func(kind, contentType string, body []byte, truncated bool) capture.Payload {
		sum := sha256.Sum256(body)
		p := capture.Payload{Kind: kind, ContentType: contentType, Body: body, OriginalBytes: int64(len(body)), SHA256: sum[:]}
		if truncated {
			p.Body, p.Truncated = body[:len(body)/2], true
		}
		return p
	}
	h.mu.Lock()
	h.clock = h.clock.Add(time.Second)
	at := h.clock
	h.mu.Unlock()
	g := capture.Generation{ID: uuid.New(), Time: at, ConversationID: turn.conversation, Status: capture.StatusCompleted,
		Credential: capture.Credential{Provider: "codex"}, TokenSource: capture.TokensReported, Tokens: usage.TokenBreakdown{Quality: "complete"},
		Payloads: []capture.Payload{
			payload(capture.KindClientInput, clientContentType, client, turn.truncated),
			payload(capture.KindUpstreamInput, upstreamContentType, upstream, false),
			payload(capture.KindUpstreamResponse, clientContentType, []byte(`{"ok":true}`), false),
		}}
	e := &Entry{Generation: g}
	for _, p := range g.Payloads {
		e.Payloads = append(e.Payloads, StoredPayload{Payload: p, Compressed: h.encoder.EncodeAll(p.Body, nil)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := h.pg.Write(ctx, e); err != nil {
		return err
	}
	type stored struct {
		id   int64
		kind string
	}
	rows, err := h.pool.Query(ctx, `SELECT payload_id, kind FROM generation_payloads WHERE generation_id = $1`, g.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
		var v stored
		return v, r.Scan(&v.id, &v.kind)
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range ids {
		for _, p := range g.Payloads {
			if p.Kind == v.kind && p.Kind != capture.KindUpstreamResponse {
				h.sent[v.id] = sentInput{body: p.Body, contentType: p.ContentType, sha256: hex.EncodeToString(p.SHA256), bytes: p.OriginalBytes}
			}
		}
	}
	return nil
}

func (h *pruneHarness) write(turn prunedTurn) {
	h.t.Helper()
	if err := h.store(turn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pruneHarness) pass() pruning.Result {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	return pruning.Run(ctx, h.prune, 50)
}

// payloadState is one stored input's pruning columns.
type payloadState struct {
	id, base, restSource int64
	failures             int
	evaluated            bool
}

// states returns a conversation's inputs of one kind, oldest first.
func (h *pruneHarness) states(conversation, kind string) []payloadState {
	h.t.Helper()
	rows, err := h.pool.Query(context.Background(), `SELECT payload_id, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0),
		prefix_failures, prefix_evaluated_at IS NOT NULL FROM generation_payloads
		WHERE conversation_id = $1 AND kind = $2 ORDER BY created_at, payload_id`, conversation, kind)
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (payloadState, error) {
		var s payloadState
		return s, r.Scan(&s.id, &s.base, &s.restSource, &s.failures, &s.evaluated)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *pruneHarness) mostFailures(conversation string) int {
	h.t.Helper()
	most := 0
	for _, kind := range pruning.Kinds {
		for _, s := range h.states(conversation, kind) {
			most = max(most, s.failures)
		}
	}
	return most
}

// table renders every stored payload's body and pruning columns, to show a
// pass changed nothing.
func (h *pruneHarness) table(withEvaluation bool) string {
	h.t.Helper()
	columns := `payload_id, encode(body, 'hex'), content_type, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0)`
	if withEvaluation {
		columns += `, prefix_failures, coalesce(prefix_evaluated_at::text, '')`
	}
	var out string
	if err := h.pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(concat_ws('|', `+columns+`), E'\n' ORDER BY payload_id), '')
		FROM generation_payloads`).Scan(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// body returns a stored payload's decompressed body and columns.
func (h *pruneHarness) body(id int64) (body []byte, contentType string, base, restSource int64) {
	h.t.Helper()
	var compressed []byte
	if err := h.pool.QueryRow(context.Background(), `SELECT body, content_type, coalesce(base_payload_id, 0), coalesce(rest_source_payload_id, 0)
		FROM generation_payloads WHERE payload_id = $1`, id).Scan(&compressed, &contentType, &base, &restSource); err != nil {
		h.t.Fatal(err)
	}
	body, err := h.decoder.DecodeAll(compressed, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return body, contentType, base, restSource
}

// storedFacts are the columns that describe an input as it was sent.
type storedFacts struct {
	id          int64
	contentType string
	sha256      string
	bytes       int64
}

// verify checks every stored input: a full one holds what was sent, a
// residual rebuilds the RFC 8785 serialization of what was sent, and each
// keeps the original's facts. It returns how many inputs are residuals.
func (h *pruneHarness) verify() int {
	h.t.Helper()
	rows, err := h.pool.Query(context.Background(), `SELECT payload_id, original_content_type, body_sha256, original_bytes
		FROM generation_payloads WHERE kind <> 'upstream_response'`)
	if err != nil {
		h.t.Fatal(err)
	}
	stored, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedFacts, error) {
		var f storedFacts
		return f, r.Scan(&f.id, &f.contentType, &f.sha256, &f.bytes)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(stored) != len(h.sent) {
		h.t.Fatalf("%d inputs stored, want the %d sent", len(stored), len(h.sent))
	}
	residuals := 0
	for _, f := range stored {
		sent, ok := h.sent[f.id]
		if !ok || f.contentType != sent.contentType || f.sha256 != sent.sha256 || f.bytes != sent.bytes {
			h.t.Fatalf("payload %d facts = %+v, want those of the input sent", f.id, f)
		}
		body, contentType, base, restSource := h.body(f.id)
		if base == 0 {
			if !bytes.Equal(body, sent.body) || contentType != sent.contentType {
				h.t.Fatalf("full payload %d = %s (%s), want it as sent", f.id, body, contentType)
			}
			continue
		}
		residuals++
		if contentType != pruning.ContentType {
			h.t.Fatalf("residual %d content type = %q", f.id, contentType)
		}
		residual, err := pruning.ParseResidual(body)
		if err != nil {
			h.t.Fatalf("residual %d: %v", f.id, err)
		}
		if (restSource == 0) != (len(residual.Rest) > 0) || (restSource != 0 && restSource != base) {
			h.t.Fatalf("residual %d rest source = %d with base %d and rest %s", f.id, restSource, base, residual.Rest)
		}
		baseBody, _, baseBase, _ := h.body(base)
		if baseBase != 0 {
			h.t.Fatalf("residual %d has base %d, which is itself a residual", f.id, base)
		}
		transcript, err := pruning.Project(baseBody)
		if err != nil {
			h.t.Fatal(err)
		}
		rebuilt, err := pruning.Rebuild(residual, transcript, transcript.Rest)
		if err != nil {
			h.t.Fatalf("residual %d: %v", f.id, err)
		}
		original, err := pruning.Parse(sent.body)
		if err != nil {
			h.t.Fatal(err)
		}
		if !bytes.Equal(rebuilt, original.Canonical()) {
			h.t.Fatalf("residual %d rebuilds %s, want %s", f.id, rebuilt, original.Canonical())
		}
	}
	return residuals
}

// requireChain fails unless the conversation's newest input of each kind
// is full and every earlier one is a residual based on it.
func (h *pruneHarness) requireChain(conversation string) {
	h.t.Helper()
	for _, kind := range pruning.Kinds {
		states := h.states(conversation, kind)
		newest := states[len(states)-1]
		for _, s := range states {
			want := newest.id
			if s.id == newest.id {
				want = 0
			}
			if s.base != want || !s.evaluated || s.failures != 0 {
				h.t.Fatalf("%s %s input %d = %+v, want base %d, evaluated, no failures", conversation, kind, s.id, s, want)
			}
		}
	}
}

func TestPruningRebuildsEveryTurn(t *testing.T) {
	h := newPruneHarness(t)
	brief, thorough := `"instructions":"be brief",`, `"instructions":"be thorough",`
	// Turn 3 changes the rest, so turn 1, which inherits turn 2's rest,
	// keeps that rest in full when rebased onto turn 3.
	rests := []string{brief, brief, thorough, thorough}
	for i, rest := range rests {
		h.write(prunedTurn{conversation: "chat", items: 2*i + 1, rest: rest})
		if res := h.pass(); res.Outcome != pruning.OutcomeOK {
			t.Fatalf("pass after turn %d = %+v", i+1, res)
		}
		h.requireChain("chat")
		if got := h.verify(); got != 2*i {
			t.Fatalf("after turn %d, %d residuals, want %d", i+1, got, 2*i)
		}
		if i == 2 {
			for _, kind := range pruning.Kinds {
				first := h.states("chat", kind)[0]
				if first.restSource != 0 {
					t.Fatalf("rebased %s input %d takes its rest from %d, want its own rest", kind, first.id, first.restSource)
				}
			}
		}
	}
	for _, kind := range pruning.Kinds {
		if third := h.states("chat", kind)[2]; third.restSource != third.base {
			t.Fatalf("%s input %d shares its base's rest but takes it from %d", kind, third.id, third.restSource)
		}
	}

	// A truncated input stays full and is no base.
	h.write(prunedTurn{conversation: "cut", items: 1, rest: brief, truncated: true})
	h.write(prunedTurn{conversation: "cut", items: 3, rest: brief})
	if res := h.pass(); res.Outcome != pruning.OutcomeOK || res.Pruned[capture.KindUpstreamInput] != 1 || res.Pruned[capture.KindClientInput] != 0 {
		t.Fatalf("pass over the truncated conversation = %+v", res)
	}
	if first := h.states("cut", capture.KindClientInput)[0]; first.base != 0 || !first.evaluated {
		t.Fatalf("truncated input = %+v, want full and evaluated", first)
	}
	h.verify()

	// Inputs without a conversation ID stay full, are marked evaluated,
	// and leave the pass's next run nothing to do.
	h.write(prunedTurn{items: 1, rest: brief})
	h.write(prunedTurn{items: 3, rest: brief})
	if res := h.pass(); res.Outcome != pruning.OutcomeOK || res.Pruned[capture.KindClientInput]+res.Pruned[capture.KindUpstreamInput] != 0 {
		t.Fatalf("pass over inputs without a conversation = %+v", res)
	}
	var unconversed, unmarked int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE base_payload_id IS NOT NULL
			OR prefix_evaluated_at IS NULL OR prefix_evaluation_version <> $1 OR prefix_failures <> 0)
		FROM generation_payloads WHERE conversation_id IS NULL AND kind <> 'upstream_response'`, pruning.EvaluationVersion).Scan(&unconversed, &unmarked); err != nil {
		t.Fatal(err)
	}
	if unconversed != 4 || unmarked != 0 {
		t.Fatalf("%d of %d inputs without a conversation are not full and evaluated", unmarked, unconversed)
	}
	h.verify()

	// A rerun changes nothing.
	before := h.table(true)
	if res := h.pass(); res.Outcome != pruning.OutcomeOK || res.Pruned[capture.KindClientInput]+res.Pruned[capture.KindUpstreamInput] != 0 {
		t.Fatalf("rerun = %+v, want nothing pruned", res)
	}
	if h.table(true) != before {
		t.Fatal("a rerun changed stored payloads")
	}

	// An interval of 0 starts no schedule.
	svc := &Service{Pool: h.pool}
	if err := svc.StartPruning(0, 50); err != nil || svc.pruning != nil {
		t.Fatalf("StartPruning(0) = %v with schedule %v, want no schedule", err, svc.pruning)
	}
}

func TestPruningMismatchLeavesBodiesIntact(t *testing.T) {
	h := newPruneHarness(t)
	h.write(prunedTurn{conversation: "chat", items: 1})
	h.write(prunedTurn{conversation: "chat", items: 3})
	// The injected check verifies residuals whose digest is wrong.
	h.prune.check = func(rep pruning.Replacement, bodies map[int64][]byte) error {
		var residual map[string]any
		if err := json.Unmarshal(rep.Body, &residual); err != nil {
			return err
		}
		residual["canonical_sha256"] = strings.Repeat("0", 64)
		tampered, err := json.Marshal(residual)
		if err != nil {
			return err
		}
		rep.Body = tampered
		return pruning.Check(rep, bodies)
	}
	before := h.table(false)
	if res := h.pass(); res.Outcome != pruning.OutcomePartial || len(res.Failed) != 1 {
		t.Fatalf("pass with a mismatch = %+v, want the conversation failed", res)
	}
	if h.table(false) != before {
		t.Fatal("a failed verification changed stored bodies")
	}
	if got := h.mostFailures("chat"); got != 1 {
		t.Fatalf("failures = %d, want 1", got)
	}
	h.prune.check = pruning.Check
	if res := h.pass(); res.Outcome != pruning.OutcomeOK {
		t.Fatalf("pass = %+v", res)
	}
	h.requireChain("chat")
	h.verify()
}

func TestPruningQuarantinesFailingConversations(t *testing.T) {
	h := newPruneHarness(t)
	failing := false
	h.prune.fail = func(conversation string) error {
		if failing && conversation == "poison" {
			return fmt.Errorf("evaluation of %s failed", conversation)
		}
		return nil
	}
	cleanItems := 1
	// passWith runs a pass after a new turn of the clean conversation and
	// requires the clean conversation pruned whatever the poison does.
	passWith := func() pruning.Result {
		t.Helper()
		h.write(prunedTurn{conversation: "clean", items: cleanItems})
		cleanItems += 2
		res := h.pass()
		h.requireChain("clean")
		return res
	}

	h.write(prunedTurn{conversation: "poison", items: 1})
	failing = true
	if res := passWith(); res.Outcome != pruning.OutcomePartial || h.mostFailures("poison") != 1 {
		t.Fatalf("failing pass = %+v with %d failures", res, h.mostFailures("poison"))
	}
	failing = false
	if res := passWith(); res.Outcome != pruning.OutcomeOK || h.mostFailures("poison") != 0 {
		t.Fatalf("succeeding pass = %+v with %d failures", res, h.mostFailures("poison"))
	}
	// Failure, success, and failure leave one consecutive failure.
	h.write(prunedTurn{conversation: "poison", items: 3})
	failing = true
	if res := passWith(); res.Outcome != pruning.OutcomePartial || h.mostFailures("poison") != 1 {
		t.Fatalf("failing pass = %+v with %d failures, want 1", res, h.mostFailures("poison"))
	}
	// Two more consecutive failures quarantine the conversation's
	// unevaluated inputs of both kinds.
	passWith()
	res := passWith()
	if res.Quarantined[capture.KindClientInput] != 1 || res.Quarantined[capture.KindUpstreamInput] != 1 || h.mostFailures("poison") != pruning.QuarantineFailures {
		t.Fatalf("third failing pass = %+v with %d failures, want the second turn quarantined", res, h.mostFailures("poison"))
	}
	for _, kind := range pruning.Kinds {
		for _, s := range h.states("poison", kind) {
			if s.base != 0 || !s.evaluated {
				t.Fatalf("quarantined %s input = %+v, want full and evaluated", kind, s)
			}
		}
	}
	// A quarantined conversation is left out of later passes.
	if res = passWith(); res.Outcome != pruning.OutcomeOK || len(res.Failed) != 0 || h.mostFailures("poison") != pruning.QuarantineFailures {
		t.Fatalf("pass after quarantine = %+v", res)
	}
	// A new turn makes the next pass evaluate it again.
	failing = false
	h.write(prunedTurn{conversation: "poison", items: 5})
	if res = passWith(); res.Outcome != pruning.OutcomeOK {
		t.Fatalf("pass after a new turn = %+v", res)
	}
	h.requireChain("poison")
	h.verify()
}

func TestPruningBesideLiveWrites(t *testing.T) {
	h := newPruneHarness(t)
	const turns = 30
	h.write(prunedTurn{conversation: "chat", items: 1})
	done := make(chan error, 1)
	go func() {
		for i := 1; i < turns; i++ {
			if err := h.store(prunedTurn{conversation: "chat", items: 2*i + 1}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	passes := 0
	for writing := true; writing; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			writing = false
		default:
			if res := h.pass(); res.Outcome != pruning.OutcomeOK {
				t.Fatalf("pass beside writes = %+v", res)
			}
			passes++
		}
	}
	if passes == 0 {
		t.Fatal("no pass ran while generations were written")
	}
	if res := h.pass(); res.Outcome != pruning.OutcomeOK {
		t.Fatalf("final pass = %+v", res)
	}
	h.requireChain("chat")
	if got := h.verify(); got != 2*(turns-1) {
		t.Fatalf("%d residuals, want %d", got, 2*(turns-1))
	}
}
