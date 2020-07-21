
GOLANGCI_LINT_VERSION=v1.23.7

LINTER=./bin/golangci-lint
LINTER_VERSION_FILE=./bin/.golangci-lint-version-$(GOLANGCI_LINT_VERSION)

GORELEASER_VERSION=v0.123.3

SHELL=/bin/bash

LINTER=./bin/golangci-lint

TEST_COVERAGE_REPORT_FILE ?= coverage.txt

test:
	go test -race -v $$(go list ./... | grep -v /vendor/)

test-with-coverage:
	go test -race -v -covermode=atomic -coverpkg=./... -coverprofile $(TEST_COVERAGE_REPORT_FILE) $$(go list ./... | grep -v /vendor/)

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

.PHONY: docker lint publish release test test-centos test-debian test-docker test-all test-docker-standalone
