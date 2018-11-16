# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM golang:1.10.3-alpine as builder

RUN apk --no-cache add \
    libc-dev \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/src/gopkg.in/launchdarkly/ld-relay.v5

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPATH=/go

RUN go build -a -o ldr ./cmd/ld-relay

FROM alpine:3.7

RUN addgroup -g 1000 -S ldr-user && \
    adduser -u 1000 -S ldr-user -G ldr-user && \
    mkdir /ldr && \
    chown 1000:1000 /ldr

RUN apk add --no-cache \
    curl \
    ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/src/gopkg.in/launchdarkly/ld-relay.v5

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

COPY docker-entrypoint.sh /usr/bin/

ENTRYPOINT ["docker-entrypoint.sh"]

USER 1000

EXPOSE 8030
CMD ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf"]
