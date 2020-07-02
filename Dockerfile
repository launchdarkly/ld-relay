# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM cimg/go:1.13 as builder

ARG SRC_DIR=$HOME/ld-relay

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPATH=/go

RUN go build -a -o ldr ./cmd/ld-relay

FROM cimg/go:1.13

RUN addgroup -g 1000 -S ldr-user && \
    adduser -u 1000 -S ldr-user -G ldr-user && \
    mkdir /ldr && \
    chown 1000:1000 /ldr

RUN apt-get install \
    curl \
    ca-certificates \
 && update-ca-certificates

ARG SRC_DIR=$HOME/ld-relay

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

USER 1000

EXPOSE 8030
ENV PORT=8030
ENTRYPOINT ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf", "--allow-missing-file", "--from-env"]
