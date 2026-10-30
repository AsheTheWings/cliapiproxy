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
| `GET /config`, `GET /config/<path>` | The effective configuration in the v8 layout, or one subtree of it. Client and upstream API keys show only their last four characters, or none when shorter than 16; the management key, the proxy URL, and relay credentials are left out. Each configured API key carries the `auth_index` of its credential. |
| `PUT /config/access/api-keys` | Replace the client API keys and their names. The body is the whole list in order, each item a key, which has no name, or `{"api-key": "<key>", "name": "<name>"}`; `GET /config/access` shows the keys under `api-keys` and their names under `api-key-names`. Replacing an item in place changes a key and keeps its position. An empty list refuses every proxy request. |
| `PUT /config/api-keys/<family>` | Replace a family's groups: `openai-compatibility`, `meta`, `commandcode`, or `kiro`. The body is the family's whole list as `GET /config/api-keys/<family>` shows it. |
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

## Dashboard

The server serves a management dashboard at `/management.html`, a single page
built from [`dashboard/`](../dashboard) into the binary. It signs in with the
management key and works through the API above. Its pages give an overview of
the keys and credentials, add, name, edit, and revoke client API keys, edit
each family's upstream groups, manage credential files, run OAuth logins, and
show the server's version and connection. The page answers 404 while the
management API is off, and when the binary was built without it, as a plain
`go build` is; [building](building.md#dashboard) describes building it in.
