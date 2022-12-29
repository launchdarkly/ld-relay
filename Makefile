
GOLANGCI_LINT_VERSION=v1.48.0

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

OPTIONAL_TAGS_PARAM=$(if ${TAGS},-tags ${TAGS},)
ALL_TEST_TAGS=big_segment_external_store_tests,integrationtests,redis_unit_tests

build:
	go build .

test:
	go test -run=not-a-real-test -tags $(ALL_TEST_TAGS) ./...  # just ensures that the tests compile
	go test -race -v $(OPTIONAL_TAGS_PARAM) ./...

test-coverage: $(COVERAGE_PROFILE_RAW)
	if [ ! -x "$(GOPATH)/bin/go-coverage-enforcer)" ]; then go install github.com/launchdarkly-labs/go-coverage-enforcer@latest; fi
	$(GOPATH)/bin/go-coverage-enforcer $(COVERAGE_ENFORCER_FLAGS) -outprofile $(COVERAGE_PROFILE_FILTERED) $(COVERAGE_PROFILE_RAW) || true
	@# added || true because we don't currently want go-coverage-enforcer to stop the build due to coverage gaps
	go tool cover -html $(COVERAGE_PROFILE_FILTERED) -o $(COVERAGE_PROFILE_FILTERED_HTML)
	go tool cover -html $(COVERAGE_PROFILE_RAW) -o $(COVERAGE_PROFILE_RAW_HTML)

integration-test:
	go test -v -tags integrationtests ./integrationtests

benchmarks: build
	go test -benchmem '-run=^$$' '-bench=.*' ./...

$(COVERAGE_PROFILE_RAW): $(ALL_SOURCES)
	@mkdir -p ./build
	go test -run=not-a-real-test -tags $(ALL_TEST_TAGS) ./...  # just ensures that the tests compile
	go test $(OPTIONAL_TAGS_PARAM) -coverprofile $(COVERAGE_PROFILE_RAW) -coverpkg=./... ./...

$(LINTER_VERSION_FILE):
	rm -f $(LINTER)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s $(GOLANGCI_LINT_VERSION)
	touch $(LINTER_VERSION_FILE)

lint: $(LINTER_VERSION_FILE)
	$(LINTER) run ./...

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=<(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $$2 $$5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)

echo-release-notes:
	@cat $(RELEASE_NOTES)

RELEASE_CMD=curl -sL https://git.io/goreleaser | GOPATH=$(mktemp -d) VERSION=$(GORELEASER_VERSION) bash -s -- --rm-dist --release-notes $(RELEASE_NOTES)

# Note that we're setting GOPATH to a temporary location when running goreleaser, because
# we want it to start from a clean state even if we've previously run a build - and also
# because during a release, we may need to run this command under another account and we
# don't want to mess up file permissions in the regular GOPATH.
publish:
	./scripts/run-goreleaser.sh $(GORELEASER_VERSION)

products-for-release:
	./scripts/run-goreleaser.sh $(GORELEASER_VERSION) --skip-publish --skip-validate

DOCKER_COMPOSE_TEST=docker-compose -f docker-compose.test.yml

test-centos test-debian test-docker test-docker-standalone: products-for-release
	$(DOCKER_COMPOSE_TEST) up --force-recreate -d $(subst test,relay,$@)
	trap "$(DOCKER_COMPOSE_TEST) logs && $(DOCKER_COMPOSE_TEST) rm -f" EXIT; $(DOCKER_COMPOSE_TEST) run --rm $@

docker-smoke-test: test-centos test-debian test-docker test-docker-standalone

.PHONY: docker build lint publish products-for-release test test-centos test-debian test-docker test-all test-docker-standalone integration-test benchmarks
