# How does the server use Kiro accounts?

Kiro is a provider like Codex or Kimi: a Kiro login is a credential file in
the auth directory, and requests for `kiro/<model>` run on that account. The
server speaks to Kiro's Responses-style inference route, so every client API
(Chat Completions, Responses, Claude Messages, Gemini) reaches it through the
usual translators.

## Logging in

`-kiro-login` runs the device login: the server prints a verification URL and
code, waits while the user approves it in a browser, writes the credential
file, and exits. `-kiro-import FILE` writes the same credential from the
refresh token that FILE holds; the file must be accessible by its owner only.
Both commands refresh the token once and load the account's model catalog
before they write, and both name the file after the account's profile, so
logging in again with the same account replaces its file.

The credential file holds the access and refresh tokens, the expiry, the
account's profile ARN, and the model catalog (`kiro_catalog`). The server
refreshes the token ten minutes before its expiry, since the service can reject
a token earlier than its stated expiry. A refresh keeps the refresh token, which
the service does not rotate, and loads the catalog again; when the catalog
cannot be loaded, the previous one stays. A request the service rejects for an
invalid token refreshes the credential once and runs again.

## Models

Plans differ in the models they include, so each credential publishes the
models of its own catalog and nothing else. A model's public id is
`kiro/<model id>`, for example `kiro/claude-haiku-5.5`. The catalog also sets each model's
context window, output limit, image input, and reasoning levels. A credential
without a catalog in its file publishes no models until its next refresh.

A model's reasoning setting follows the effort values its catalog schema lists.
Claude models take `adaptive` reasoning with an effort or `disabled`; the other
families with an effort schema take an effort and, if the schema lists `none`,
can run without reasoning. A request that names no effort gets the schema's
default, as the Kiro agent does. The server checks a client's effort against
these levels with its shared thinking rules: a level the model lacks is refused,
or moved to the nearest level the model has when the client's API format belongs
to another provider family than the model's (Claude Messages to Kimi, for
example). A request
to turn reasoning off on a model that cannot is run at the model's lowest
level. Models without an effort schema receive no reasoning setting.

## What the service receives

The server sends the headers and body fields the Kiro agent sends: `model`,
`input`, `tools`, `stream`, `origin`, `profileArn`, `metadata`, and `reasoning`.
The values come from one pinned release of the agent, named by the `CLIVersion`
and `AgentVersion` constants of
[`internal/auth/kiro`](../internal/auth/kiro/fingerprint.go). The TLS handshake
and HTTP framing are those of the server's HTTP stack, not of the agent's.

`x-amzn-codewhisperer-optout` is always `true`, on inference and catalog
requests. The agent sends `false` when its user allows content collection, so
this header is a deliberate privacy setting of the server, not agent parity.

The inference route accepts no system text, tool choice, sampling parameter,
output limit, or non-streaming call, so the server maps a request onto it:

- System and developer text before the first turn becomes a leading user turn
  followed by the assistant turn `I will follow these instructions.`; later
  system text becomes a user turn. Neighbouring turns of one role are joined.
- Only function tools are sent. Hosted tools (web search, image generation) and
  freeform tools are left out; the `apply_patch` tool is bridged to a function
  tool.
- Images must be data URIs.
- A reasoning item is replayed only when it is a value the service produced
  (a JSON object with a signature) and the model has an effort schema; any other
  reasoning item is omitted. Claude Messages clients do not carry that value
  back, so their conversations replay no reasoning.
- Each run of tool calls is sent as the calls followed by one result per call in
  call order; a call without a result, a result without a call, and a repeated
  result are dropped.
- A non-streaming client call runs as a stream and is collected.

`metadata` names the conversation the way the agent does. The conversation id
(`sess_<uuid>`) derives from the client's session identity, so one session keeps
one conversation id across its requests, and the continuation id changes with
each user turn. A request without a session identity gets a conversation of its
own. The ids are sent for every model; GPT and Kimi models depend on the
conversation id for prompt caching.

## Usage and errors

The service reports a request's cost in credits and the share of the model's
input window the request used, and no token counts. The response keeps the
credits (`usage["kiro:credits"]`) for Responses clients, and the other client
APIs receive token counts only; the input token count is that share of the
catalog's input window, an estimate, and the output token count is 0. A turn
that calls tools ends with the status `requires_action`, which the server
reports as `completed`.

The service reports a rejected access token as 403 `access_denied`; the server
reports it as 401 so the credential refreshes. Other service errors keep their
status and body.

## Limits

- A response whose body ends without `response.completed`,
  `response.incomplete`, or an error event fails with a 408 error, also for a
  stream that has already sent text.
- `-kiro-import` checks POSIX file modes, so it refuses token files on systems
  that report other modes.
- Throttling, quota exhaustion, and errors inside an open stream have not been
  observed on the service, so they are not classified apart from their status.
- The device login keeps polling on an answer without tokens and on a
  `pending` or `slow_down` error, and stops on any other client error or when
  the code expires. The service's answer before approval has not been observed
  in the pinned release.
- Only the Google identity provider is requested at login; `-kiro-import`
  accepts the refresh token of any login.
- Token counting requests are answered by a local estimate.
