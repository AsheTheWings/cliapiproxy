# How do I run the server's container image?

[`compose.yaml`](../compose.yaml) is the reference deployment, following the
Compose Specification. It runs the server from the repository's image with
the production profile, a read-only root filesystem, and host networking, so
the server can reach an OpenTelemetry collector on the host's loopback while
it listens on `127.0.0.1` itself.

The server takes its inputs as [configuration](configuration.md) describes:

- The environment sets the profile, the files, and the telemetry endpoint and
  environment.
- The overlay, a Compose config, sets the port and the auth directory.
- The protected file, `protected.env` beside `compose.yaml`, is a Compose
  secret at `/run/secrets/protected`. It holds the client API keys and any
  OTLP headers, and must be readable by the server's UID 65532 alone:

  ```sh
  CLIPROXY_API_KEYS='<client key>'
  ```

- The `auths` volume at `/data/auths` holds the credential files, which are
  state the server writes as it refreshes tokens.
- The `providers` volume at `/data/providers` holds the provider state file,
  `providers.yaml`, which the management API writes.

A login command runs in the same container with its flag appended, for
example `docker compose run --rm cliapiproxy -codex-device-login`.
[Observability](observability.md) describes the telemetry inputs.
