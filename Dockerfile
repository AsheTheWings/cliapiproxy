# Base images are pinned by digest; change a pin only together with its tag.
FROM docker.io/oven/bun:1.3.14@sha256:e10577f0db68676a7024391c6e5cb4b879ebd17188ab750cf10024a6d700e5c4 AS dashboard

WORKDIR /src

COPY dashboard/package.json dashboard/bun.lock dashboard/
RUN cd dashboard && bun install --frozen-lockfile

COPY dashboard/ dashboard/
COPY scripts/dashboard.sh scripts/
RUN mkdir -p internal/dashboard/dist

ARG VERSION=0.0.0
RUN sh scripts/dashboard.sh "$VERSION"

FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=dashboard /src/internal/dashboard/dist/management.html internal/dashboard/dist/management.html

ARG VERSION=0.0.0
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/cliapiproxy ./cmd/server/ \
    && mkdir -p /out/data/auths /out/data/keys

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /out/cliapiproxy /usr/local/bin/cliapiproxy
# A volume mounted here starts owned by the server's user.
COPY --from=builder --chown=65532:65532 /out/data /data

USER nonroot:nonroot
EXPOSE 8317
ENTRYPOINT ["/usr/local/bin/cliapiproxy"]
