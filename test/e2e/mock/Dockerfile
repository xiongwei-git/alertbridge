FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /mockfeishu main.go

FROM scratch
COPY --from=build /mockfeishu /mockfeishu
USER 10002:10002
EXPOSE 9090
ENTRYPOINT ["/mockfeishu"]
