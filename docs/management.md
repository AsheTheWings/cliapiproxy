# How do I manage the server while it runs?

The management API under `/v8/management` shows the configuration the server
runs with and manages the credentials in its auth directory. It is off unless
the protected file sets `CLIPROXY_MANAGEMENT_KEY`; then every request
presents that key as `Authorization: Bearer <key>` or `X-Management-Key`,
and five failed attempts from one address block it for 30 minutes. The
server keeps only the key's bcrypt hash. It accepts requests from loopback
addresses alone unless `management.allow-remote` is true, which a client
reaching the listener through address translation needs.

The server's inputs own its configuration, so the API reads it and never
changes it:

| Route | Does |
| --- | --- |
| `GET /config`, `GET /config/<path>` | The effective configuration in the v8 layout, or one subtree of it. Client and upstream API keys show only their last four characters, or none when shorter than 16; the management key, the proxy URL, and relay credentials are left out. Each configured API key carries the `auth_index` of its credential. |
| `GET /credentials`, `POST /credentials`, `DELETE /credentials` | List, upload, and delete credential files. |
| `GET /credentials/models`, `GET /credentials/download` | A credential's models, and a credential file's content. |
| `PATCH /credentials/status`, `PATCH /credentials/fields`, `POST /credentials/refresh` | Disable or enable a credential file, change its fields, and refresh its token. An API key the inputs configure answers 409 to a status change. |
| `GET /oauth/auth-url`, `GET /oauth/status`, `DELETE /oauth/session`, `/oauth/callback` | Run a provider's OAuth flow; a completed flow writes a credential file. |
| `POST /oauth/import`, `POST /oauth/siwc/bind`, `GET /oauth/siwc/status` | Import a credential, and bind a Codex account through Sign in with ChatGPT. |
| `POST /routing/cooldown/reset` | Clear a credential's quota cooldown. |
| `GET /routing/model-definitions/<channel>`, `POST /models/refresh` | A provider's catalog models, and an immediate catalog refresh. |

A credential file written through the API takes effect as any other change to
the auth directory does, without a restart.
