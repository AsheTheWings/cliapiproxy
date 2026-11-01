# How does the server keep a record of its generations?

The server keeps its durable record in a PostgreSQL 18 database, the store,
which the `store.*` inputs name ([configuration](configuration.md#store)).

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
