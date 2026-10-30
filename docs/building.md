# How do I build CLIProxyAPI?

The server binary builds from `cmd/server` with Go at the version `go.mod`
names:

```sh
go build -o CLIProxyAPI ./cmd/server
```

## Dashboard

The management dashboard is a React application in `dashboard/`, built with
Bun at the version its `package.json` names into one HTML page that the
server embeds. [`scripts/dashboard.sh`](../scripts/dashboard.sh) installs its
locked dependencies, runs its tests, lint, and type check, builds the page,
and copies it to `internal/dashboard/dist/management.html`, which Git
ignores. A `go build` after it embeds the page; one without it builds a server
whose `/management.html` answers 404.

```sh
scripts/dashboard.sh
go build -o CLIProxyAPI ./cmd/server
```

For working on the dashboard, `bun run dev` in `dashboard/` serves it with
hot reload; it signs in to any running server by its URL and management key.

## Container image

The image builds from the repository's `Dockerfile` on pinned base images: a
Bun stage runs `scripts/dashboard.sh`, and the Go stage embeds its page into a
static binary on distroless, run as the image's `nonroot` user (UID 65532),
with `/data/auths` as its auth directory's mount point.
[`scripts/image.sh`](../scripts/image.sh) builds it with Podman into an OCI
layout, stamping the commit into the binary and fixing every timestamp at the
commit time, then writes that image's config and layers as one manifest
annotated with the `org.opencontainers.image` revision, source, run URL,
creation time, and version. The version is the fixed `0.0.0` placeholder; the
server reports `0.0.0+<commit>` as its `service.version`.

```sh
created=$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)
scripts/image.sh build build-layout "$(git rev-parse HEAD)" "$created"
ORAS=<oras> scripts/image.sh push build-layout "$(git rev-parse HEAD)" "$created" \
  <source-url> <run-url> --oci-layout image-layout
```

The push prints the manifest digest; the same build layout and arguments
always produce the same one. [Testing](testing.md) describes the test
classes, the fast gate, and the qualification of the image layout.

## Continuous integration

The `Image` workflow runs on GitHub-hosted runners. Every run checks out the
commit, runs the fast gate and the integration class, builds the image,
annotates it into a layout, and qualifies that layout. Pull request runs stop
there and hold no token. Push runs on any branch, and manual runs, then push
the qualified image to the registry the repository variable
`ARTIFACT_REGISTRY` names, authenticating with the job's OIDC token: runs on
the default branch to `<registry>/cliapiproxy/default`, other branches to
`<registry>/cliapiproxy/branches`, tagged
`<commit>-<run id>-<run attempt>`. The publishing job fails unless the
pushed manifest is the qualified one, and logs its digest as
`SHA-256: <hex>`.

[Container](container.md) describes running the image.
