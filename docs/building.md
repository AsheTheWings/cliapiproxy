# How do I build CLIProxyAPI?

The server binary builds from `cmd/server` with Go at the version `go.mod`
names:

```sh
go build -o CLIProxyAPI ./cmd/server
```

The container image builds from the repository's `Dockerfile`, which takes
the version, commit, and build date it stamps into the binary as build
arguments:

```sh
podman build \
  --build-arg VERSION=0.0.0 \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t cliproxyapi .
```

[Testing](testing.md) describes the test classes and the fast gate.
