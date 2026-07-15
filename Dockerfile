# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
ARG VERSION=dev
FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
ARG VERSION
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
    -o /out/alertbridge ./cmd/alertbridge && \
    mkdir -p /out/data

FROM scratch
ARG VERSION
LABEL org.opencontainers.image.title="AlertBridge" \
      org.opencontainers.image.description="Lightweight secure notification gateway" \
      org.opencontainers.image.source="https://github.com/xiongwei-git/alertbridge" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/alertbridge /alertbridge
COPY --from=build --chown=10001:0 /out/data /var/lib/alertbridge

USER 10001:0
EXPOSE 8080
ENTRYPOINT ["/alertbridge"]
