#!/bin/bash

# By default, Go projects use a "v" prefix for release tags ("v5.0.0").
#
# In the past, some of our projects were tagged without this prefix, which is a problem if you are using Go modules.
# If we need to continue to *also* add tags in the old format, we can do so via a custom release-tags.sh in those
# projects.

echo "v${LD_RELEASE_VERSION}"
