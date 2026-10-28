# How do I build CLIProxyAPI?

The server binary builds from `cmd/server` with Go at the version `go.mod`
names:

```sh
go build -o CLIProxyAPI ./cmd/server
```

## Container image

The image builds from the repository's `Dockerfile` on pinned base images: a
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
