# How are CLIProxyAPI's tests organized and run?

Every `_test.go` file that holds tests declares one class in its `//go:build`
constraint. A plain `go test ./...` selects no class and runs nothing.

| Class | Boundary | Command |
| --- | --- | --- |
| unit | Pure code over in-memory values: no filesystem, sockets, processes, process configuration, or wall-clock waits | `scripts/test.sh unit` |
| component | In-process code that may use run-owned temporary files, loopback sockets, the process environment, and bounded waits | `scripts/test.sh component` |
| conformance | Read-only checks of the source tree, including that every test file's class covers what it does | `scripts/test.sh conformance` |
| integration | Code that starts processes, such as plugin builds, or needs real network interfaces, such as WebRTC media | `scripts/test.sh integration` |

The fast hermetic gate runs unit, component, and conformance:

```sh
scripts/test.sh fast
```

`scripts/test.sh` passes `go test` flags and package patterns through, for
example `scripts/test.sh component -run '^TestName$' ./internal/watcher`. It
runs the tests with only the Go toolchain's location and caches, the module
proxy, and a run-owned home and temporary directory as inputs, so no
developer or production configuration reaches them.

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
