# Orgin of Docker Image: https://github.com/Azure/azure-cli/blob/dev/Dockerfile
FROM mcr.microsoft.com/azure-cli:latest

USER root

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT [ "/entrypoint.sh" ]