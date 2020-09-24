
.PHONY: build

# In Relay v5, we need to be able to build in older Go versions that don't support modules. Therefore,
# this test code has been modified to use the older non-module version of go-test-helpers, and its
# dependencies have been vendored in. We also need to copy the code to another location in GOPATH so
# the compiler will not consider it to be part of the Relay project and will use its vendor/ instead.

BUILD_DIR=$(GOPATH)/src/github.com/launchdarkly/testservice

build:
	mkdir -p $(BUILD_DIR)
	cp -r * $(BUILD_DIR)
	cd $(BUILD_DIR) && go build -o testservice
	cp $(BUILD_DIR)/testservice .
