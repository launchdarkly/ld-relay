#!/bin/sh

AZURE_TENANT_ID="8afe73f9-0d93-4821-a898-c5c2dc320953"
AZURE_CLIENT_ID="6e260531-60cf-4e62-b354-f85f1b768768"  #Platform-Permissions

ACR_SUBSCRIPTION_ID="98dcb0c8-8268-4a0a-b2d2-92006dc489e7"

run_build() {

  az login --service-principal --username "$AZURE_CLIENT_ID" --password "$CLIENT_SECRET" --tenant "$AZURE_TENANT_ID"

  az account set -s $ACR_SUBSCRIPTION_ID

  echo "Container tag: $INPUT_REPO:$INPUT_IMAGETAG "

  az acr build -t $INPUT_REPO:$INPUT_IMAGETAG -r r1k8sacrdev --file Dockerfile.relativity --no-wait .

}

main() {

  run_build "$@" 2>&1 | tee -a result

}

set -o pipefail

main "$@"
exit $?