FROM centos

COPY dist/ld-relay*amd64.rpm .

RUN rpm -Uvh ld-relay*amd64.rpm

RUN systemctl enable ld-relay.service

EXPOSE 8030

CMD /sbin/init
