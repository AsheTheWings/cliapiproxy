# How does the server use Kiro accounts?

Kiro is a provider like Codex or Kimi: a Kiro account is an API key the
server keeps, and requests for `kiro/<model>` run on that account. The
server speaks to Kiro's Responses-style inference route, so every client API
(Chat Completions, Responses, Claude Messages, Gemini) reaches it through the
usual translators.

## Accounts

An account is an API key that Kiro issues for it. The keys are the API-key
family `kiro` of the [key state](configuration.md#keys): groups under
`api-keys.kiro`, each with a `name`, an optional `prefix`, and its keys. A
group takes no `base-url`, because the service is fixed and each key's
account decides its models. A group's `models` select among them. Several
keys in one group balance requests across their accounts:

```yaml
api-keys:
  kiro:
    - name: kiro
      keys: [{api-key: <key>}, {api-key: <key>}]
```

Each key becomes a credential the server keeps in memory and never writes to
the auth directory. Every request carries the key as a Bearer token with the
header `TokenType: API_KEY`, from which the service derives the account and its
profile, so the server sends no profile. At start, and then every hour, the
server reloads the account's model catalog (`ListAvailableModels` on the
`us-east-1` management host), which also shows that the service still accepts
the key; a reload that cannot load the catalog keeps the previous one, and
without one it fails and runs again five minutes later. A key the service
rejects fails the reload with 401. An API key has no expiry and is
never refreshed. A change to the keys takes effect without a restart. An auth-directory file of
type `kiro` holds no usable credential; the server ignores it with a warning.

## Models

Plans differ in the models they include, so each credential publishes the
models of its own catalog and nothing else. A model's public id is
`kiro/<model id>`, for example `kiro/claude-haiku-5.5`. The catalog also sets each model's
context window, output limit, image input, and reasoning levels. A credential
publishes no models until its first catalog load succeeds.

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
`input`, `tools`, `stream`, `origin`, `metadata`, and `reasoning`.
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

The service reports a rejected key as 403 `access_denied`; the server reports
it as 401. Other service errors keep their status and body.

## Limits

- A response whose body ends without `response.completed`,
  `response.incomplete`, or an error event fails with a 408 error, also for a
  stream that has already sent text.
- The server uses the `us-east-1` hosts. The Kiro agent probes other regions
  for a key's home region; a key of another region is not supported.
- Throttling, quota exhaustion, and errors inside an open stream have not been
  observed on the service, so they are not classified apart from their status.
- Token counting requests are answered by a local estimate.
