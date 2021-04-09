# This is a standalone Dockerfile that does not depend on goreleaser building the binary
# It is NOT the version that is pushed to dockerhub
FROM golang:1.15.2-alpine as builder

RUN apk --no-cache add \
    libc-dev \
    # TEMPORARY FOR FEATURE BRANCH: we need to include git in order to use prerelease dependencies
    git openssh-client \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/ld-relay

RUN mkdir -p $SRC_DIR

WORKDIR $SRC_DIR

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPATH=/go

# TEMPORARY FOR FEATURE BRANCH: allow fetching of prerelease dependencies - the SSH configuration
# will be copied into the project directly by the CI script
RUN mv temp_ssh ~/.ssh
RUN chmod 400 ~/.ssh/id_rsa*
RUN git config --global url.git@github.com:.insteadOf https://github.com/
ENV GOPRIVATE=github.com/launchdarkly/*-private

RUN go build -a -o ldr .

FROM alpine:3.12.0

RUN addgroup -g 1000 -S ldr-user && \
    adduser -u 1000 -S ldr-user -G ldr-user && \
    mkdir /ldr && \
    chown 1000:1000 /ldr

RUN apk add --no-cache \
    curl \
    ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/cache/apk/*

ARG SRC_DIR=/go/ld-relay

COPY --from=builder ${SRC_DIR}/ldr /usr/bin/ldr

USER 1000

EXPOSE 8030
ENV PORT=8030
ENTRYPOINT ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf", "--allow-missing-file", "--from-env"]
