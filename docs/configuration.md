# How is the server configured?

Every input the server reads is declared in
[`internal/inputs/schema.json`](../internal/inputs/schema.json): its source,
its shape, whether it is configuration or protected, and its default in each
profile. Protected inputs grant access or identity, and the server never
prints them. The tracked [`.env`](../.env) is generated from the schema by
`scripts/generate-env.sh` and lists the environment inputs with their
development defaults; it is for reading, and nothing loads it.

## Sources and layers

Each input has one source:

- an environment variable or a command-line flag, the process inputs;
- a key: a configuration key comes from the YAML overlay files, and a
  protected key from the protected file's line of its secret name.

Values resolve in layers, lowest first: the schema default for the profile,
the overlay files, the protected file, and the process inputs. Every layer is
optional, and an input without a value in any of them is required.
`CLIPROXY_PROFILE` selects the profile, `development` or `production`, and has
no default. A default can derive from another environment input, as the
development auth directory derives from `XDG_STATE_HOME`; production defaults
leave paths and ports to the target.

`CLIPROXY_OVERLAY_FILES` names the overlay files, colon-separated; a later file
overrides an earlier one. They nest the keys by their dotted path:

```yaml
server:
  port: 8317
```

`CLIPROXY_PROTECTED_FILE` names the protected file. It holds one
`NAME='value'` line per protected input, named by the input's `secret` in the
schema, with blank lines and `#` comments allowed. A value holds no quote or
line break, and a list is comma-separated:

```sh
CLIPROXY_API_KEYS='<key>,<key>'
```

## Validation

The server validates the effective inputs before it starts any work and
reports every problem at once, naming inputs and files but never values. A
run fails when:

- a required input has no value, or a value does not have its input's shape;
- an overlay file holds a key the schema does not declare, a key that does not
  apply to the profile, or a protected key;
- the protected file holds a name that is not a protected input's, a name
  twice, or a line of another form;
- an overlay or protected file is present but unreadable or malformed;
- the protected file is accessible by anyone but its owner.

An absent file is an absent layer. An environment variable set to the empty
string counts as unset.

`-validate` stops there: it prints `valid` and exits 0 when the effective
inputs are valid, and otherwise reports the problems and exits 2, so a
deploy can check a target's inputs before it starts the server.

## Upstream API keys

Each upstream API-key family the schema declares pairs two inputs: an
`upstream-groups` input in the overlay files, keyed `api-keys.<family>`, and
the `credential-list` input its `credentials` names in the protected file.
A group has a `name` of lowercase letters, digits, and hyphens, and may set
`base-url`, `prefix`, and `models`. A model has a `name` and may set `alias`,
`display-name`, `max-context-length`, `thinking.levels`, and, for
OpenAI-compatible groups, `input-modalities`. An OpenAI-compatible group
requires `base-url` and at least one model, because the server offers only
the models such a group lists. A group of another family without models
offers its provider's catalog: the built-in Meta catalog, or for
[Command Code](commandcode.md) and [Kiro](kiro.md), the catalog of each key's
account. A group with models offers only those it lists, and a listed model
takes every property it does not set from its catalog entry. A Command Code
or Kiro group takes no `base-url`, and it offers a listed model only while
the account's catalog has it; a model's `name` is its id in that catalog, as
`/v1/models` lists it. Its `thinking.levels` change the levels it publishes,
but a request still reaches the upstream with only an effort the catalog
lists for the model:

```yaml
api-keys:
  openai-compatibility:
    - name: example
      base-url: https://api.example.com/v1
      models:
        - name: vendor/model-1
          alias: model-1
          max-context-length: 200000
          input-modalities: [text, image]
          thinking: {levels: [low, medium, high]}
  meta:
    - name: muse
      models: [{name: muse-spark-1.3}]
  commandcode:
    - name: cc
      models: [{name: commandcode/deepseek/deepseek-v4.1-flash}]
```

The keys are comma-separated `group=key` items, and a group with several
keys balances requests across them:

```sh
CLIPROXY_OPENAI_COMPATIBILITY_KEYS='example=<key>,example=<key>'
CLIPROXY_META_KEYS='muse=<key>'
CLIPROXY_COMMANDCODE_KEYS='cc=<key>'
```

A run fails when a group has no key or an overlay group holds a key or a
field outside this list. A key of a group the overlay does not declare is
unused, so keys can be delivered before the overlay that declares their
group.

## OAuth models

`oauth.models.<provider>` lists, by catalog id, the models the OAuth
credential files of one provider offer: `antigravity`, `claude`, `codex`,
`devin`, `kimi`, `kimi-ai`, `meta`, or `xai`. A listed model keeps every
property its catalog entry gives it, and ids match without regard to case.
An empty list, the default, offers the provider's whole catalog. The lists
apply to credential files only; an API-key group lists its own models:

```yaml
oauth:
  models:
    codex: [gpt-6-sol, gpt-6-luna]
```

## Model prefixes

A group's `prefix`, or the `prefix` field of an OAuth credential file,
publishes each of its models a second time as `<prefix>/<model>`, and a
request for that name uses only the credentials with that prefix. An
unprefixed name is shared by every credential serving the model, whatever
its provider. With `routing.force-model-prefix` set, a prefixed
credential's models are published and routed only under their prefixed
names, so giving every credential a prefix makes each published model
name select exactly one provider.

## During a run

Once its inputs are resolved, the server clears its environment, so no part of
it reads an environment variable the schema has not validated; upstream
requests use a proxy only through `requests.proxy-url`. The configuration is
fixed for the run, so applying a change takes a restart, and the management
API is disabled. [Observability](observability.md) describes the telemetry
inputs. Credential files are state, not configuration: the server
watches the auth directory, and changes to it take effect immediately.

## Changing an input

Change the schema, run `scripts/generate-env.sh`, and extend the test in
`cmd/server` that checks every keyed input reaches the server's
configuration.
