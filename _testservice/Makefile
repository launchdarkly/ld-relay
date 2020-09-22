
.PHONY: build

BUILD_DIR=$(GOPATH)/src/github.com/launchdarkly/testservice

build:
	mkdir -p $(BUILD_DIR)
	cp -r * $(BUILD_DIR)
	cd $(BUILD_DIR) && go build -o testservice
	cp $(BUILD_DIR)/testservice .
