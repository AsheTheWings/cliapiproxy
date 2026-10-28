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
- a key of the YAML files: configuration keys come from the overlay files,
  protected keys from the protected file.

Values resolve in layers, lowest first: the schema default for the profile,
the overlay files, the protected file, and the process inputs. Every layer is
optional, and an input without a value in any of them is required.
`CLIPROXY_PROFILE` selects the profile, `development` or `production`, and has
no default. A default can derive from another environment input, as the
development auth directory derives from `XDG_STATE_HOME`; production defaults
leave paths and ports to the target.

`CLIPROXY_OVERLAY_FILES` names the overlay files, colon-separated; a later file
overrides an earlier one. `CLIPROXY_PROTECTED_FILE` names the protected file.
Both files nest the keys by their dotted path:

```yaml
server:
  port: 8317
```

## Validation

The server validates the effective inputs before it starts any work and
reports every problem at once, naming inputs and files but never values. A
run fails when:

- a required input has no value, or a value does not have its input's shape;
- a file holds a key the schema does not declare, a key that does not apply to
  the profile, or a key of the other class;
- an overlay or protected file is present but unreadable or malformed;
- the protected file is accessible by anyone but its owner.

An absent file is an absent layer. An environment variable set to the empty
string counts as unset.

## During a run

Once its inputs are resolved, the server clears its environment, so no part of
it reads an environment variable the schema has not validated; upstream
requests use a proxy only through `requests.proxy-url`. The configuration is
fixed for the run, so applying a change takes a restart, and the management
API is disabled. Credential files are state, not configuration: the server
watches the auth directory, and changes to it take effect immediately.

## Changing an input

Change the schema, run `scripts/generate-env.sh`, and extend the test in
`cmd/server` that checks every keyed input reaches the server's
configuration.
