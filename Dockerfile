FROM alpine:3.6

RUN apk add --no-cache \
    curl \
    ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/cache/apk/*

COPY ldr /usr/bin/
COPY docker-entrypoint.sh /usr/bin/

ENTRYPOINT ["docker-entrypoint.sh"]

EXPOSE 8030
CMD ["ldr"]
