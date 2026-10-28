# syntax=docker/dockerfile:1

# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM golang:1.27.3-bookworm as builder
# See "Runtime platform versions" in CONTRIBUTING.md

ARG SRC_DIR=/go/ld-relay

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPATH=/go

RUN go build -a -o ldr .

FROM gcr.io/distroless/static-debian13:nonroot

ARG SRC_DIR=/go/ld-relay

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

EXPOSE 8030
ENV PORT=8030
ENTRYPOINT ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf", "--allow-missing-file", "--from-env"]
