# How are CLIProxyAPI's tests organized and run?

Every `_test.go` file that holds tests declares one class in its `//go:build`
constraint. A plain `go test ./...` selects no class and runs nothing.

| Class | Boundary | Command |
| --- | --- | --- |
| unit | Pure code over in-memory values: no filesystem, sockets, processes, process configuration, or wall-clock waits | `scripts/test.sh unit` |
| component | In-process code that may use run-owned temporary files, loopback sockets, the process environment, and bounded waits | `scripts/test.sh component` |
| conformance | Read-only checks of the source tree, including that every test file's class covers what it does | `scripts/test.sh conformance` |
| integration | Code that starts processes, such as plugin builds, or needs real network interfaces, such as WebRTC media | `scripts/test.sh integration` |
| qualification | The built container image, run from its OCI layout with Podman | `scripts/test.sh qualification ./test/qualification` |

The dashboard's Bun tests in `dashboard/tests` are unit tests and
read-only conformance checks of its sources and locale files.
[`scripts/dashboard.sh`](../scripts/dashboard.sh) runs them with its lint, type
check, and build, and the image build runs that script, so every image build
gates on them. One file runs with `bun test tests/<name>.test.ts` in
`dashboard/`.

The fast hermetic gate runs unit, component, and conformance:

```sh
scripts/test.sh fast
```

`scripts/test.sh` passes `go test` flags and package patterns through, for
example `scripts/test.sh component -run '^TestName$' ./internal/watcher`. It
runs the tests with only the Go toolchain's location and caches, the module
proxy, and a run-owned home and temporary directory as inputs, so no
developer or production configuration reaches them.

## Qualifying the image

Qualification runs the exact image layout [building](building.md) produces,
named by `CLIPROXY_QUALIFICATION_LAYOUT`, and checks it carries the revision
`CLIPROXY_QUALIFICATION_REVISION` names; those two values are its only inputs
beyond the toolchain. It loads the image into a run-owned Podman store,
serves it with production inputs against a run-owned OTLP endpoint, checks
authentication, the embedded dashboard page, a clean exit on SIGTERM, and the exported traces and metrics,
runs the telemetry probe, and removes the store.

```sh
CLIPROXY_QUALIFICATION_LAYOUT=image-layout CLIPROXY_QUALIFICATION_REVISION="$(git rev-parse HEAD)" \
  scripts/test.sh qualification ./test/qualification
```

## Classifying a test

Declare the narrowest class that covers what the file does, combined with
any existing constraint, for example `//go:build linux && integration`. A file
also does what the test files it references in its directory do, so files
that share declarations share a class. The conformance check in
[`internal/testsupport/testclass`](../internal/testsupport/testclass) reports
each file whose class is missing or too narrow, with the calls behind it.

## Waiting

Tests never sleep for a fixed duration. They wait on an observable condition
under a bounded deadline with
[`internal/testsupport/wait`](../internal/testsupport/wait): `wait.Until`
and `wait.Poll` for polled state, `wait.Receive` for channels,
`wait.GoroutineIn` for work parked at a known function, and `wait.Settled`
before checking that a forbidden event has not happened. Time-dependent
logic that runs in process runs inside a `testing/synctest` bubble, whose
synthetic clock advances exactly. A fake whose reply must outlast a real
deadline may sleep on a line marked `// fixture latency:` with its reason.
