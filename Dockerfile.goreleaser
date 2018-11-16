# This is the Dockerfile used for release (published to dockerhub by goreleaser)

FROM alpine:3.7

RUN apk add --no-cache \
    curl \
    ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/cache/apk/*

COPY ld-relay /usr/bin/ldr

RUN addgroup -g 1000 -S ldr-user && \
    adduser -u 1000 -S ldr-user -G ldr-user && \
    mkdir /ldr && \
    chown 1000:1000 /ldr

COPY docker-entrypoint.sh /usr/bin/

ENTRYPOINT ["docker-entrypoint.sh"]

USER 1000

EXPOSE 8030
CMD ["/usr/bin/ldr", "--config", "/ldr/ld-relay.conf"]
