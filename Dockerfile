# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM cimg/go:1.13 as builder

ARG SRC_DIR=~/ld-relay

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

RUN go build -a -o ldr ./cmd/ld-relay

FROM cimg/go:1.13

RUN sudo addgroup --gid 1000 --system ldr-user && \
    sudo adduser --system --uid 1000 --gid 1000 ldr-user && \
    sudo mkdir /ldr && \
    sudo chown 1000:1000 /ldr

RUN sudo apt-get install \
    curl \
    ca-certificates \
 && update-ca-certificates

ARG SRC_DIR=~/ld-relay

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

USER 1000

EXPOSE 8030
ENV PORT=8030
ENTRYPOINT ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf", "--allow-missing-file", "--from-env"]
