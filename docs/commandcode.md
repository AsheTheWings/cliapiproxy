# How does the server use Command Code accounts?

Command Code is a provider like Codex or Kimi: a Command Code account is an
API key in the server's inputs, and requests for `commandcode/<model id>` run
on that account. The server speaks to the
gateway's agent route (`/alpha/generate`) and converts its stream to a
Responses stream, so every client API (Chat Completions, Responses, Claude
Messages, Gemini) reaches it through the usual translators.

## Accounts

The gateway has no login the server can run; an account is the API key
Command Code issues for it. The keys are the API-key family `commandcode`
([configuration](configuration.md#upstream-api-keys)): an overlay declares
groups under `api-keys.commandcode`, each with a `name` and an optional
`prefix`, and the protected file's `CLIPROXY_COMMANDCODE_KEYS` holds their
`group=key` items. A group takes no `base-url` or `models`, because the
gateway is fixed and each key's account decides its models. Several keys in
one group balance requests across their accounts:

```yaml
api-keys:
  commandcode:
    - name: cc
      prefix: cc
```

```sh
CLIPROXY_COMMANDCODE_KEYS='cc=<key>,cc=<key>'
```

Each key becomes a credential the server keeps in memory and never writes to
the auth directory. At start, and then every hour, the server reloads the
account's plan, credits, and model catalog, which also shows that the gateway
still accepts the key; a reload that cannot load the catalog keeps the
previous one, and without one it fails and runs again five minutes later. A
key the gateway rejects with 401 fails the reload with that
status. An API key has no expiry. Changing the keys takes a restart.

## Models

The catalog lists every model of the gateway, and the gateway checks the plan
when a request arrives. The server therefore applies the plan rules of the
Command Code CLI to the catalog and publishes a credential's allowed models only:

- An account with purchased or free credits may use every model.
- An account without a known plan may use every model. The plan is the
  subscription's plan when its status is `active`, `trialing`, or `past_due`.
- Otherwise a model with a minimum plan is allowed when the account's plan
  ranks at least as high; a model without one is allowed when the plan includes
  the model's category (`opensource` or `premium`). Go plans include
  `opensource` models, and the other plans include both categories.

A model's public id is `commandcode/<gateway id>`, for example
`commandcode/deepseek/deepseek-v4-flash`. The catalog sets each model's context
window, output limit, input modalities, and reasoning efforts. A model's
efforts, without `off`, are its reasoning levels; a model that lists `off` can
run without reasoning. A model without efforts has no reasoning setting. A
credential publishes no models until a reload has loaded its catalog, and a
reloaded catalog replaces its published models.

Free models appear and retire without a catalog change, so a catalog entry can
fail at request time; the gateway's error reaches the client.

## What the gateway receives

The server sends the headers and body fields the CLI sends to `/alpha/generate`.
The values come from one pinned release, named by the constants of
[`internal/auth/commandcode`](../internal/auth/commandcode/fingerprint.go). The
TLS handshake and HTTP framing are those of the server's HTTP stack.

The request carries these deliberate differences from the CLI:

- `x-taste-learning` is always `false`. The CLI sends `true`, which lets the
  gateway learn from the conversation.
- The working directory is the neutral `/work`, and the repository, memory,
  taste, and skill fields are empty.
- The request has no `temperature`; the CLI sends none either.
- The request has no `traceparent`. The CLI sends its own trace context; the
  server's traces end at its upstream calls, for every provider.

The route has no tool choice, so the server maps a request onto it:

- System and developer text before the first turn becomes the `system` blocks.
  The gateway applies its own coding-agent prompt when a request has no system
  text, so a request with none receives the system text `You are a helpful
  assistant.`. A later system message becomes user text.
- `max_tokens` is the client's limit, else 64000, kept within the model's output
  limit and the gateway's maximum of 200000.
- Only function tools are sent. Hosted and freeform tools are left out; the
  `apply_patch` tool is bridged to a function tool.
- Images must be data URIs, and a model whose catalog entry lacks image input
  receives a text note instead.
- `reasoning_effort` is sent only when the client names an effort the model
  lists; turning reasoning off is sent as `off` for models that list it.
- Reasoning is replayed as reasoning text: from the value the server returned
  in the reasoning item's `encrypted_content`, else from the item's summary.
- Each run of tool calls is sent as the calls followed by one result per call in
  call order; a call without a result, a result without a call, and a repeated
  result are dropped. The gateway accepts tool call ids up to 64 characters, so
  longer ids are sent as `cc-1`, `cc-2`, and so on, for the call and its result
  alike.
- A non-streaming client call runs as a stream and is collected.

`threadId` and the `x-session-id` header are one UUID. It derives from the
session identity the server's credential affinity uses. A client session id
keeps one thread across a session's requests. Without one, the identity is a
hash of the conversation's opening messages, including the first assistant
reply once the history carries it, so the first request and its follow-ups
can get different threads, and conversations that open identically share one.
A request the server derives no identity for gets a thread of its own.

## Usage and errors

The gateway reports token counts, including cached input tokens and reasoning
tokens, and the server returns them. The cost in credits is not reported on the
stream; the account's balance is visible in Command Code itself.

HTTP errors keep their status and body: 401 for a rejected key, 403 for a model
outside the plan or a client version the gateway refuses, 429 and 5xx as the
gateway sends them. An `error` event inside an accepted stream fails the request
with the event's status, or 502 when it states none. A response whose body ends
without a finish event fails with a 408 error, also for a stream that has
already sent text.

## Limits

- There is no interactive login; the key is an input.
- Image input, the Anthropic and Google model families, and the models above the
  Go plan have not been exercised against the gateway; they follow the CLI's
  documented request shapes.
- The effect of `reasoning_effort` on a response has not been verified.
- Token counting requests are answered by a local estimate.
