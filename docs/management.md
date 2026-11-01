# How do I manage the server while it runs?

The management API under `/v8/management` shows the configuration the server
runs with, manages the client and upstream API keys it keeps as
[key state](configuration.md#keys), and manages the credentials in its auth
directory. It is off unless
the protected file sets `CLIPROXY_MANAGEMENT_KEY`; then every request
presents that key as `Authorization: Bearer <key>` or `X-Management-Key`,
and five failed attempts from one address block it for 30 minutes. The
server keeps only the key's bcrypt hash. It accepts requests from loopback
addresses alone unless `management.allow-remote` is true, which a client
reaching the listener through address translation needs.

The server's inputs own the rest of its configuration, so the API reads it
and never changes it. A change to the keys or the credentials takes effect
without a restart:

| Route | Does |
| --- | --- |
| `GET /config`, `GET /config/<path>` | The effective configuration in the v8 layout, or one subtree of it. Client and upstream API keys show only their last four characters, or none when shorter than 16; the management key, the proxy URL, and relay credentials are left out. Each client and upstream key is an object with its `label`, when it has one, and its `fingerprint`, and each configured upstream key carries the `auth_index` of its credential. |
| `PUT /config/access/api-keys` | Replace the client API keys and their labels. The body is the whole list in order, each item `{"api-key": "<key>", "label": "<label>"}` with `label` optional, as `GET /config/access/api-keys` shows it; the fingerprint it shows is ignored. Replacing an item in place changes a key and keeps its position. An empty list refuses every proxy request. |
| `PUT /config/api-keys/<family>` | Replace a family's groups: `openai-compatibility`, `meta`, `commandcode`, or `kiro`. The body is the family's whole list as `GET /config/api-keys/<family>` shows it, each key with an optional `label`. |
| `GET /credentials`, `POST /credentials`, `DELETE /credentials` | List, upload, and delete credential files. |
| `GET /credentials/models`, `GET /credentials/download` | A credential's models, and a credential file's content. |
| `PATCH /credentials/status`, `PATCH /credentials/fields`, `POST /credentials/refresh` | Disable or enable a credential file, change its fields, and refresh its token. An API key of a group answers 409 to a status change; it changes through its group. |
| `GET /oauth/auth-url`, `GET /oauth/status`, `DELETE /oauth/session`, `/oauth/callback` | Run a provider's OAuth flow; a completed flow writes a credential file. |
| `POST /oauth/import`, `POST /oauth/siwc/bind`, `GET /oauth/siwc/status` | Import a credential, and bind a Codex account through Sign in with ChatGPT. |
| `POST /routing/cooldown/reset` | Clear a credential's quota cooldown. |
| `GET /routing/model-definitions/<channel>`, `POST /models/refresh` | A provider's catalog models, and an immediate catalog refresh. |

Both `PUT` routes take keys as `GET` shows them: a key sent back masked keeps
the stored key it masks, taken in order within its list, and one that no
stored key masks to answers 409. A list that breaks the rules of the
[key state](configuration.md#keys) answers 400 and changes nothing.

A credential file written through the API takes effect as any other change to
the auth directory does, without a restart.

## Usage and generations

These routes read the [store](generations.md) and answer 503 while it is
off. Times are RFC 3339 and days are UTC `YYYY-MM-DD`; a malformed
parameter answers 400 with the reason.

| Route | Does |
| --- | --- |
| `GET /quota` | Every credential the server has, then stored credentials it no longer has, each with its provider, group, account label, whether the server polls its usage, and its latest usage snapshot: the time, source, plan, and each meter's used, limit, remaining, window, and reset. |
| `POST /quota/refresh` | Poll one credential's usage source now, the body `{"credential_id": "<id>"}`, and answer the poll's outcome and the credential's latest usage. A credential without a usage source, or whose poll is running, answers 409. |
| `GET /quota/timeline?credential=<id>&from=&to=` | One credential's snapshots, by default over the last 7 days: each meter's value, its change since the previous snapshot, and the generations and tokens between the two. |
| `GET /generations` | Generations newest first, filtered by `from`, `to`, `provider`, `credential`, `model` (upstream), `effort`, `client` (key fingerprint), `conversation`, and `status`, `limit` per page (50 by default, at most 500). A page's `next_cursor`, passed as `cursor`, continues the listing. A generation with a trace carries its `trace_url` when `dashboard.trace-url-template` is set, the template's `{trace_id}` replaced by the trace ID. |
| `GET /generations/<id>` | One generation's columns, its usage before and after it, and each payload's kind, content type, sizes, and whether it is truncated or pruned. |
| `GET /generations/<id>/payloads/<kind>` | One payload decompressed with the content type it was sent with: `client_input`, `upstream_input`, or `upstream_response`. A pruned input is rebuilt as the RFC 8785 serialization of the body as sent, checked against its SHA-256, and marked `X-Payload-Rebuilt: true`; a failed check answers 500 without a body. A truncated body is marked `X-Payload-Truncated: true`. |
| `GET /generations/summary?from=&to=&group_by=` | Daily generation counts, token sums, and usage amounts over a range of days, by default the last 30, grouped by any of `credential`, `model`, `effort`, and `client`. A credential bucket carries the provider, group, and account label of its latest generation that day, and a client bucket its key label. |
| `GET /generations/latency?from=&to=` | The daily 50th, 90th, and 99th percentile time to first token per upstream model, by default over the last 30 days. |

The summary reads `generation_usage_daily`, which the server refreshes every
15 minutes, so it can trail the latest generations by that much. The latency
and every other route read the store as it is.

## Dashboard

The server serves a management dashboard at `/management.html`, a single page
built from [`dashboard/`](../dashboard) into the binary. It signs in with the
management key and works through the API above. Its pages give an overview of
the keys and credentials, add, label, edit, and revoke client API keys, edit
each family's upstream groups and their keys' labels, manage credential files, run OAuth logins, and
show the server's version and connection. While the store is on, its Quota
page shows each credential's plan and meters, polls one on request, and charts
its usage over time against the generations between snapshots. Its Generations
page lists generations with the filters above, opens each one's columns,
payloads, and trace link, and follows a conversation oldest first; its
Insights tab charts daily tokens by model, effort, or client, cache and
reasoning shares, time to first token, and usage per client, account, and
model. The page answers 404 while the
management API is off, and when the binary was built without it, as a plain
`go build` is; [building](building.md#dashboard) describes building it in.
