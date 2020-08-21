#!/bin/bash

set -eu

# The Docker tests that are run by "make integration-test" rely on being able to build Relay the same way
# a user would do from the public source repositories. However, sometimes on a development branch it is
# necessary to temporarily use not-yet-released dependencies from private repositories. In that case, the
# integration test cannot work because the Docker build does not have credentials (as a user would not)
# to access the private code. So in that scenario we will skip the integration test step. The private
# dependencies will always need to be changed back to public ones anyway prior to a release, so the tests
# will run at that point.

if grep 'replace.*=>.*-private' go.mod >/dev/null; then
  echo "Skipping integration tests because this branch is using private dependencies"
else
  make integration-test
fi
