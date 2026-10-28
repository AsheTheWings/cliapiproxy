# What telemetry does the server emit?

The server emits traces, metrics, and logs through OpenTelemetry and writes
no diagnostics of its own: log records reach no console or file, and request
logging is off. [`internal/telemetry/catalog.json`](../internal/telemetry/catalog.json)
is the versioned catalog of every span, metric, attribute, and error class;
tests fail on a signal it does not list.

## Exporters

`OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, and `OTEL_LOGS_EXPORTER` each
select `otlp`, `console`, or `none`. The OTLP exporters send OTLP/HTTP to the
signal paths under `OTEL_EXPORTER_OTLP_ENDPOINT`, with the headers of the
protected `telemetry.otlp-headers` input. Export is batched with bounded
queues and a 10-second timeout, and the server flushes on shutdown; a failed
export is dropped, never written elsewhere. [Configuration](configuration.md)
explains where these inputs come from.

Every signal carries `service.namespace` and `service.name` (`cliapiproxy`),
the build's `service.version`, a `service.instance.id` new for each process,
and `deployment.environment.name` from `CLIPROXY_DEPLOYMENT_ENVIRONMENT`.

## Signals

- Each HTTP request is a server span named for its method and route template.
  It continues a W3C `traceparent` the client sends; the server does not
  forward trace context to upstream providers.
- Each model execution is a span across its credential attempts, with the
  operation, the provider, and a stable error class.
- Log records at info level and above become LogRecords. Debug records are
  never exported, since they can carry payloads, and every exported message
  and field is scrubbed of authorization values, tokens, API keys, and
  credentials in URLs.

## Changing a signal

Change the instrumentation and the catalog together, raise the catalog's
version when a name, attribute, or error class changes meaning, and cover the
signal with a test that records it through `internal/testsupport/telemetrytest`.
