
GOLANGCI_LINT_VERSION=v1.27.0

LINTER=./bin/golangci-lint
LINTER_VERSION_FILE=./bin/.golangci-lint-version-$(GOLANGCI_LINT_VERSION)

GORELEASER_VERSION=v0.141.0

SHELL=/bin/bash

LINTER=./bin/golangci-lint

TEST_COVERAGE_REPORT_FILE ?= coverage.txt

ALL_SOURCES := $(shell find * -type f -name "*.go")
COVERAGE_PROFILE_RAW=./build/coverage_raw.out
COVERAGE_PROFILE_RAW_HTML=./build/coverage_raw.html
COVERAGE_PROFILE_FILTERED=./build/coverage.out
COVERAGE_PROFILE_FILTERED_HTML=./build/coverage.html
COVERAGE_ENFORCER_FLAGS=\
  	-skipfiles 'internal/core/sharedtest/' \
	-skipcode "// COVERAGE" -packagestats -filestats -showcode

build:
	go build ./...

test:
	go test -race -v ./...

test-coverage: $(COVERAGE_PROFILE_RAW)
	if [ ! -x "$(GOPATH)/bin/go-coverage-enforcer)" ]; then go get -u github.com/launchdarkly-labs/go-coverage-enforcer; fi
	$(GOPATH)/bin/go-coverage-enforcer $(COVERAGE_ENFORCER_FLAGS) -outprofile $(COVERAGE_PROFILE_FILTERED) $(COVERAGE_PROFILE_RAW) || true
	@# added || true because we don't currently want go-coverage-enforcer to stop the build due to coverage gaps
	go tool cover -html $(COVERAGE_PROFILE_FILTERED) -o $(COVERAGE_PROFILE_FILTERED_HTML)
	go tool cover -html $(COVERAGE_PROFILE_RAW) -o $(COVERAGE_PROFILE_RAW_HTML)

$(COVERAGE_PROFILE_RAW): $(ALL_SOURCES)
	@mkdir -p ./build
	go test -coverprofile $(COVERAGE_PROFILE_RAW) -coverpkg=./... ./...

$(LINTER_VERSION_FILE):
	rm -f $(LINTER)
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | bash -s $(GOLANGCI_LINT_VERSION)
	touch $(LINTER_VERSION_FILE)

lint: $(LINTER_VERSION_FILE)
	$(LINTER) run ./...

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=<(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $$2 $$5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)

echo-release-notes:
	@cat $(RELEASE_NOTES)

RELEASE_CMD=curl -sL https://git.io/goreleaser | VERSION=$(GORELEASER_VERSION) bash -s -- --rm-dist --release-notes $(RELEASE_NOTES)

publish:
	$(RELEASE_CMD)

release:
	$(RELEASE_CMD) --skip-publish --skip-validate

DOCKER_COMPOSE_TEST=docker-compose -f docker-compose.test.yml

test-centos test-debian test-docker test-docker-standalone: release
	$(DOCKER_COMPOSE_TEST) up --force-recreate -d $(subst test,relay,$@)
	trap "$(DOCKER_COMPOSE_TEST) logs && $(DOCKER_COMPOSE_TEST) rm -f" EXIT; $(DOCKER_COMPOSE_TEST) run --rm $@

integration-test: test-centos test-debian test-docker test-docker-standalone

.PHONY: docker build lint publish release test test-centos test-debian test-docker test-all test-docker-standalone
