//go:build integration

package generations

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations/capture"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/schema"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/fakeexec"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/pgtest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/telemetrytest"
	"github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"
	cliproxyauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var server *pgtest.Server

func TestMain(m *testing.M) { pgtest.Main(m, &server) }

const (
	clientKey    = "synthetic-client-key-0123456789"
	upstreamKey  = "synthetic-upstream-key-9876543210"
	bodySecret   = "synthetic-body-secret-5555"
	storeTimeout = 10 * time.Second
)

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := schema.Discover(schema.Migrations())
	if err != nil {
		return err
	}
	_, err = schema.Apply(ctx, pool, migrations)
	return err
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return server.Pool(t, server.Clone(t, "migrated", migrate))
}

// captured runs requests through an auth manager whose credential carries
// upstreamKey, capturing into the store behind pool.
type captured struct {
	t       *testing.T
	pool    *pgxpool.Pool
	writer  *Writer
	manager *cliproxyauth.Manager
}

func newCaptured(t *testing.T) *captured {
	t.Helper()
	pool := migratedPool(t)
	writer, err := NewWriter(NewPGStore(pool), WriterOptions{PayloadLimit: 1 << 20, QueueBytes: 1 << 26,
		ClientKeyLabel: func(key string) string {
			if key == clientKey {
				return "laptop"
			}
			return ""
		}})
	if err != nil {
		t.Fatal(err)
	}
	capture.SetSink(writer)
	t.Cleanup(func() { capture.SetSink(nil); writer.Close(storeTimeout) })
	// The executor sends the credential's key inside the body, as some
	// upstreams require, next to what the client sent.
	executor := &fakeexec.Executor{UpstreamBody: func(payload []byte, auth *cliproxyauth.Auth) []byte {
		return append(append([]byte(`{"key":"`+auth.Attributes["api_key"]+`","client":`), payload...), '}')
	}}
	auth := &cliproxyauth.Auth{ID: "fake-" + uuid.NewString(), Provider: fakeexec.Provider, Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"api_key": upstreamKey, cliproxyauth.AttributeGroup: "team", cliproxyauth.AttributeAccountLabel: "team account"}}
	return &captured{t: t, pool: pool, writer: writer, manager: fakeexec.Manager(t, executor, auth)}
}

func clientBody(model string) []byte {
	return []byte(`{"model":"` + model + `","instructions":"be brief","input":"ping ` + bodySecret + `"}`)
}

// stream runs a streaming request and reads it to its end, or until cancel
// returns true for a chunk, which then cancels the request.
func (c *captured) stream(model string, cancelAt func(chunk cliproxyexecutor.StreamChunk) bool) {
	c.t.Helper()
	ctx, cancel := context.WithCancel(usage.WithAPIKey(context.Background(), clientKey))
	defer cancel()
	result, err := c.manager.ExecuteStream(ctx, []string{fakeexec.Provider}, cliproxyexecutor.Request{Model: model, Payload: clientBody(model)},
		cliproxyexecutor.Options{Stream: true})
	if err != nil {
		c.t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if cancelAt != nil && cancelAt(chunk) {
			cancel()
		}
	}
}

// settle waits until the writer has nothing queued, so every submitted
// generation is either stored or dropped.
func (c *captured) settle() {
	c.t.Helper()
	wait.Until(c.t, storeTimeout, func() bool { return c.writer.QueueEntries() == 0 }, "the writer did not drain")
}

func (c *captured) count(query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// payloads returns each stored payload's decompressed body by kind.
func (c *captured) payloads(generationID uuid.UUID) map[string][]byte {
	c.t.Helper()
	rows, err := c.pool.Query(context.Background(), "SELECT kind, body FROM generation_payloads WHERE generation_id = $1", generationID)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	decoder, _ := zstd.NewReader(nil)
	defer decoder.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var kind string
		var body []byte
		if err = rows.Scan(&kind, &body); err != nil {
			c.t.Fatal(err)
		}
		plain, errDecode := decoder.DecodeAll(body, nil)
		if errDecode != nil {
			c.t.Fatal(errDecode)
		}
		out[kind] = plain
	}
	return out
}

func waitForGenerations(c *captured, n int) {
	c.t.Helper()
	wait.Until(c.t, storeTimeout, func() bool { return c.count("SELECT count(*) FROM generations") == n }, "the store does not hold %d generations", n)
}

func TestCompletedGenerationsAreStoredAndFailuresAreNot(t *testing.T) {
	c := newCaptured(t)
	c.stream(fakeexec.ModelComplete, nil)
	c.stream(fakeexec.ModelWebsocket, nil)
	c.stream(fakeexec.ModelIncomplete, nil)
	waitForGenerations(c, 3)

	c.stream(fakeexec.ModelFail, nil)
	c.stream(fakeexec.ModelHold, func(cliproxyexecutor.StreamChunk) bool { return true })
	c.settle()
	if n := c.count("SELECT count(*) FROM generations"); n != 3 {
		t.Fatalf("generations = %d after a failed and a disconnected request, want 3", n)
	}
	if n := c.count("SELECT count(*) FROM generation_payloads"); n != 9 {
		t.Fatalf("payloads = %d, want three per generation", n)
	}
	if n := c.count("SELECT count(*) FROM generation_payloads WHERE octet_length(body) <> stored_bytes"); n != 0 {
		t.Fatalf("%d payloads record a stored_bytes other than their stored body's length", n)
	}
	rows, err := c.pool.Query(context.Background(), "SELECT generation_id, upstream_model, status, provider, provider_group, account_label, key_fingerprint, client_key_label, total, token_source FROM generations")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	byModel := map[string]uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var model, status, provider, group, account, fingerprint, client, source string
		var tokens int64
		if err = rows.Scan(&id, &model, &status, &provider, &group, &account, &fingerprint, &client, &tokens, &source); err != nil {
			t.Fatal(err)
		}
		byModel[model] = id
		wantStatus := capture.StatusCompleted
		if model == fakeexec.ModelIncomplete {
			wantStatus = capture.StatusIncomplete
		}
		if status != wantStatus || provider != fakeexec.Provider || group != "team" || account != "team account" ||
			len(fingerprint) != 12 || client != "laptop" || tokens != 15 || source != capture.TokensReported {
			t.Errorf("%s: status %s provider %s group %s account %q key %q client %q tokens %d source %s", model, status, provider, group, account, fingerprint, client, tokens, source)
		}
	}
	ws := c.payloads(byModel[fakeexec.ModelWebsocket])
	if lines := bytes.Count(ws[capture.KindUpstreamResponse], []byte("\n")); lines != len(fakeexec.Chunks) {
		t.Fatalf("the WebSocket response holds %d frames, want %d:\n%s", lines, len(fakeexec.Chunks), ws[capture.KindUpstreamResponse])
	}
	if n := c.count("SELECT count(*) FROM system_instructions WHERE content = 'be brief'"); n != 1 {
		t.Fatalf("system instructions = %d, want the shared one stored once", n)
	}
}

func TestNoKeyOrHeaderReachesTheStore(t *testing.T) {
	c := newCaptured(t)
	c.stream(fakeexec.ModelComplete, nil)
	waitForGenerations(c, 1)
	var id uuid.UUID
	var row string
	if err := c.pool.QueryRow(context.Background(), "SELECT generation_id, row_to_json(g)::text FROM generations g").Scan(&id, &row); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{clientKey, upstreamKey, bodySecret} {
		if strings.Contains(row, secret) {
			t.Errorf("the generation row holds %q: %s", secret, row)
		}
	}
	var payloadColumns string
	if err := c.pool.QueryRow(context.Background(), "SELECT string_agg(row_to_json(p)::text, ' ') FROM (SELECT generation_id, kind, content_type, body_sha256 FROM generation_payloads) p").Scan(&payloadColumns); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payloadColumns, clientKey) || strings.Contains(payloadColumns, upstreamKey) {
		t.Errorf("payload columns hold a key: %s", payloadColumns)
	}
	payloads := c.payloads(id)
	for kind, body := range payloads {
		if bytes.Contains(body, []byte(clientKey)) || bytes.Contains(body, []byte(upstreamKey)) {
			t.Errorf("%s holds a key: %s", kind, body)
		}
		if bytes.Contains(bytes.ToLower(body), []byte("content-type")) || bytes.Contains(body, []byte("Authorization")) {
			t.Errorf("%s holds a header: %s", kind, body)
		}
		carries := kind == capture.KindClientInput || kind == capture.KindUpstreamInput
		if bytes.Contains(body, []byte(bodySecret)) != carries {
			t.Errorf("%s: holds the body's secret = %v, want %v", kind, !carries, carries)
		}
	}
}

func entry(t *testing.T, w *Writer, traceID string) capture.Generation {
	t.Helper()
	return capture.Generation{ID: uuid.New(), Time: time.Now().UTC(), TraceID: traceID, Status: capture.StatusCompleted,
		Credential: capture.Credential{Provider: "codex"}, TokenSource: capture.TokensReported, Tokens: usage.TokenBreakdown{Quality: "complete"},
		Payloads: []capture.Payload{
			{Kind: capture.KindClientInput, ContentType: "application/json", Body: []byte(`{}`), SHA256: make([]byte, 32)},
			{Kind: capture.KindUpstreamInput, ContentType: "application/json", Body: []byte(`{}`), SHA256: make([]byte, 32)},
			{Kind: capture.KindUpstreamResponse, ContentType: "application/json", Body: []byte(`{}`), SHA256: make([]byte, 32)},
		}}
}

func TestWritesAreAtomicAndRetriedExactlyOnce(t *testing.T) {
	rec := telemetrytest.Install(t)
	pool := migratedPool(t)
	store := NewPGStore(pool)
	failures := map[string]int{"generation": 1, "commit": 1}
	var partialSeen []int
	errLost := errors.New("connection lost")
	store.fail = func(stage string) error {
		if failures[stage] == 0 {
			return nil
		}
		failures[stage]--
		if stage == "generation" {
			// Another connection sees nothing of the open transaction.
			var n int
			_ = pool.QueryRow(context.Background(), "SELECT count(*) FROM generations").Scan(&n)
			partialSeen = append(partialSeen, n)
		}
		return errLost
	}
	w, err := NewWriter(store, WriterOptions{PayloadLimit: 1 << 20, QueueBytes: 1 << 26,
		Retryable: func(err error) bool { return errors.Is(err, errLost) || Retryable(err) },
		Backoff:   func(int) time.Duration { return time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(storeTimeout) })

	interrupted := entry(t, w, "")
	acknowledged := entry(t, w, "")
	violating := entry(t, w, "not-a-trace-id")
	next := entry(t, w, "")
	// Text PostgreSQL refuses, which a client controls, still stores.
	next.SystemInstruction, next.ClientUserAgent, next.UsageRaw = "keep\x00this", "agent\xff", []byte(`{"note":"\u0000"}`)
	w.Submit(interrupted)
	wait.Until(t, storeTimeout, func() bool { return w.QueueEntries() == 0 }, "the first generation was not written")
	w.Submit(acknowledged)
	w.Submit(violating)
	w.Submit(next)
	wait.Until(t, storeTimeout, func() bool { return w.QueueEntries() == 0 }, "the writer did not drain")

	if len(partialSeen) != 1 || partialSeen[0] != 0 {
		t.Fatalf("generations visible during the interrupted write = %v, want 0", partialSeen)
	}
	for _, g := range []capture.Generation{interrupted, acknowledged, next} {
		var rows, payloads int
		if err = pool.QueryRow(context.Background(), "SELECT count(*), (SELECT count(*) FROM generation_payloads WHERE generation_id = $1) FROM generations WHERE generation_id = $1",
			g.ID).Scan(&rows, &payloads); err != nil || rows != 1 || payloads != 3 {
			t.Errorf("generation %s: %d rows, %d payloads, %v; want one row and three payloads", g.ID, rows, payloads, err)
		}
	}
	var stored int
	if err = pool.QueryRow(context.Background(), "SELECT count(*) FROM generations").Scan(&stored); err != nil || stored != 3 {
		t.Fatalf("generations = %d, %v; want 3 and the violating one dropped", stored, err)
	}
	if got := terminalDrops(t, rec); got != 1 {
		t.Fatalf("terminal_write drops = %d, want 1", got)
	}
}

func terminalDrops(t *testing.T, rec *telemetrytest.Recorder) int64 {
	t.Helper()
	var n int64
	for _, m := range rec.Metrics(t) {
		if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "cliapiproxy.store.dropped" {
			for _, p := range sum.DataPoints {
				if v, _ := p.Attributes.Value("cliapiproxy.store.drop_reason"); v.AsString() == "terminal_write" {
					n += p.Value
				}
			}
		}
	}
	return n
}
