FROM alpine

RUN apk --update upgrade && \
    apk add curl ca-certificates && \
    update-ca-certificates && \
    rm -rf /var/cache/apk/*

ADD ldr /

CMD ["/ldr", "--config=config/ld-relay.conf"]
