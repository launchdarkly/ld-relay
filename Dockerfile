# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM golang:1.17.11-alpine3.16 as builder
# See "Runtime platform versions" in CONTRIBUTING.md

RUN apk --no-cache add \
    libc-dev \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/ld-relay

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPATH=/go

RUN go build -a -o ldr .

FROM alpine:3.16.0

RUN addgroup -g 1000 -S ldr-user && \
    adduser -u 1000 -S ldr-user -G ldr-user && \
    mkdir /ldr && \
    chown 1000:1000 /ldr

RUN apk add --no-cache \
    ca-certificates \
 && apk add --upgrade libcrypto1.1 libssl1.1 \
 && update-ca-certificates \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/ld-relay

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

USER 1000

EXPOSE 8030
ENV PORT=8030
ENTRYPOINT ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf", "--allow-missing-file", "--from-env"]
