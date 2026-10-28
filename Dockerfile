# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:9048d5e7751706200eb1719c7dae0436950d362f29a1da91fa05e5c63867c60f AS build

WORKDIR /build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local TMPDIR=/build/work/tmp GOTMPDIR=/build/work/tmp
RUN mkdir -p work/tmp
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=3.0.0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION}" -o /out/shareto ./cmd/shareto

RUN mkdir -p /runtime/config/shareto /runtime/data/shareto /runtime/run/shareto /runtime/files /runtime/home/shareto
COPY packaging/docker/config.example.json /runtime/config/shareto/config.json

FROM scratch
ARG VERSION=3.0.0
LABEL org.opencontainers.image.title="shareto" \
      org.opencontainers.image.description="File sharing with expiration, download limits and passwords" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/shareto /usr/local/bin/shareto
COPY --from=build --chown=65532:65532 /runtime/ /
COPY LICENSE /usr/share/licenses/shareto/LICENSE
ENV HOME=/home/shareto XDG_CONFIG_HOME=/config XDG_STATE_HOME=/data XDG_RUNTIME_DIR=/run PATH=/usr/local/bin
USER 65532:65532
WORKDIR /files
EXPOSE 38080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/usr/local/bin/shareto", "list"]
ENTRYPOINT ["/usr/local/bin/shareto"]
CMD ["daemon"]
