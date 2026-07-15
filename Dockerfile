# syntax=docker/dockerfile:1.7
FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
    -o /out/alertbridge ./cmd/alertbridge && \
    mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/alertbridge /alertbridge
COPY --from=build --chown=10001:0 /out/data /var/lib/alertbridge

USER 10001:0
EXPOSE 8080
ENTRYPOINT ["/alertbridge"]
