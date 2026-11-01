# How does the server keep a record of its generations?

The server keeps its durable record in a PostgreSQL 18 database, the store,
which the `store.*` inputs name ([configuration](configuration.md#store)).

## Which requests are stored

A generation is one model execution that completed: a 2xx response whose
stream reached its terminal event, a complete non-stream body, or one turn
of a Responses WebSocket session that reached its terminal response event.
An execution that stopped at the output limit, such as a Responses
`response.incomplete` event or a `max_tokens` or `length` stop, is stored
with status `incomplete`, and every other one with status `completed`. A
failed request, a failed attempt, and a request whose client went away
before the end are not stored; their failures are in
[telemetry](observability.md). Token-counting requests are not
generations.

When retries move a request across credentials, the generation describes
the attempt that completed. Its three payloads hold bodies only, never
headers: the client's request body, the body sent upstream, and the
upstream response as it arrived, a stream or WebSocket session one event or
frame per line. A credential value the server put into the upstream body is
replaced with `[credential]`; what the client put into its own body is kept.
A body larger than `store.payload-limit-bytes` is kept up to that size and
marked truncated, and its size and SHA-256 describe the whole body.

Generations reach the store through a queue that one writer drains, so the
store never delays or fails a response. The queue holds at most 1,000
generations and `store.queue-bytes` of compressed payloads; a generation
that does not fit is dropped. The writer retries a lost connection or a
serialization failure until the store answers, drops a generation whose
write fails three times with any other error, and at shutdown writes for up
to 10 seconds before it drops what remains. Every drop is counted in
telemetry; a crash loses the queue uncounted.

`account_label` is an API key's label, or its group name and fingerprint,
and an OAuth credential's label, email, or project ID. `client_key_label`
is the client key's label, or its fingerprint. The server estimates Kiro's
token counts, so its generations have `token_source` `estimated`. `usage_raw`
merges the response's usage objects in order, so a Claude stream keeps both
its input usage and its final output usage. `usage_amount` holds Kiro's
`kiro:credits` and, from an OpenRouter base URL, `usage.cost` in `credits`,
and Command Code's gateway cost in `usd`.

## Usage snapshots

A usage snapshot is what a credential's provider reported about its plan
and usage at one moment. The server stores one only when the readings
differ from the credential's latest snapshot, compared by the SHA-256 of
the plan and every meter's values, window, and reset.

The server polls each credential's usage source once its requests have
paused for 10 seconds, or 30 seconds for Command Code, whose counters settle
about 20 seconds after a call. Each credential with a source is also polled
at least every 15 minutes, at an offset that spreads credentials across
that period. A credential runs one poll at a time, and a poll that takes
more than 15 seconds is abandoned. A poll that fails, or whose answer the
server cannot read, stores nothing.

| Provider | Source |
| --- | --- |
| Kiro | `GetUsageLimits`: the subscription as the plan, each usage entry and its overage cap |
| Command Code | `/alpha/billing/subscriptions` and `/alpha/billing/credits`: the plan, monthly, purchased, and free credits, and the five-hour and weekly windows |
| Claude | OAuth usage: the five-hour and seven-day windows and extra usage; each response's `anthropic-ratelimit-unified-*` headers |
| Codex | `wham/usage`: the plan, its windows, and credits; each response's `x-codex-*` headers |
| Kimi | the coding usage and its windows |
| xAI | weekly and monthly billing and the subscription tier |
| Meta | the Muse Code key route with the OAuth file's `dca_token`, without the key it returns |
| Devin | `GetUserStatus`: the plan and the daily and weekly quota |
| Antigravity | the quota summary's buckets |

Each fetcher sends the credential's own token only to its provider's fixed
URLs; a base URL in the credential or the configuration never changes them.
Kiro and Command Code are read with the credential's API key, and the
others with an OAuth credential's access token, so a Claude or Codex API
key has no usage source.

A response's headers become a snapshot with source `response_headers` in
the same transaction as its generation. The generation's
`prior_snapshot_id` names the credential's latest snapshot before the
generation started, and `response_snapshot_id` names the snapshot from its
own headers when they changed the readings.

## Tables and views

| Relation | Holds |
| --- | --- |
| `generations` | One row per stored generation, keyed by `generation_id`: its time, trace ID, conversation, client key label and fingerprint, provider, group, account label, credential, models, reasoning effort, service tiers, token breakdown, usage amount, timing, and the usage snapshots before and after it. No process deletes a row. |
| `generation_payloads` | A generation's `client_input`, `upstream_input`, and `upstream_response`, one row of each kind, zstd-compressed, with the SHA-256 and size of the uncompressed body. |
| `system_instructions` | Each distinct system instruction once, keyed by the SHA-256 of its text. |
| `credential_usage_snapshots` | A credential's plan and usage, one row per changed reading, from polling or from response headers. |
| `credential_usage_meters` | Each snapshot's meters, each in its own unit: `credits`, `percent`, `usd`, or `requests`. |
| `generation_usage_daily` | A materialized view of generations, token sums, and usage amounts per UTC day, provider, group, credential, account label, upstream model, reasoning effort, and client key. |
| `credential_usage_timeline` | A view of each meter's change since the credential's previous snapshot, in the meter's unit, with the generations and tokens between the two snapshots. |
| `schema_migrations` | The migration ledger. |

## Schema migrations

The server binary embeds the store's migrations, SQL files named
`<position>_<name>.sql` in `internal/generations/schema/migrations`, and
applies them only when asked:

```sh
cliapiproxy -migrate-status   # prints each applied, pending, and drifted migration
cliapiproxy -migrate          # applies the pending ones, printing each name
```

`-migrate-status` writes nothing and exits 0 when the schema matches the
binary and 3 when a migration is pending or drifted. `-migrate` applies
each pending migration in its own transaction together with its ledger row,
which records its name, position, and SHA-256, and stops at the first failure
without recording it. Both refuse to run when two files share a position.
`-migrate` applies nothing while the ledger shows drift: an applied
migration whose file changed or is gone, or a file missing from the ledger
below the highest applied position. Drift is repaired with a new migration
that brings the schema to the intended state, never by editing an applied
file.

A server with the store on refuses to start while a migration is pending or
drifted, so a deploy runs `-migrate` before it starts the new binary.

## Reading the store

The role `cliapiproxy_reader` can log in and select every table and view of
the store, including those later migrations add, and has no other privilege.
It has no password until the operator sets one.
