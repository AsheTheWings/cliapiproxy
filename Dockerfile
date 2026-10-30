# Base images are pinned by digest; change a pin only together with its tag.
FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=0.0.0
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/cliapiproxy ./cmd/server/ \
    && mkdir -p /out/data/auths /out/data/providers

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /out/cliapiproxy /usr/local/bin/cliapiproxy
# A volume mounted here starts owned by the server's user.
COPY --from=builder --chown=65532:65532 /out/data /data

USER nonroot:nonroot
EXPOSE 8317
ENTRYPOINT ["/usr/local/bin/cliapiproxy"]
