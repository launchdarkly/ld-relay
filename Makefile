GOLANGCI_VERSION=v1.10.2
# earlier versions of golangci-lint don't work in go 1.9

GORELEASER_VERSION=0.117.1

SHELL=/bin/bash

LINTER=./bin/golangci-lint

test:
	go test ./...

lint: $(LINTER)
	$(LINTER) run ./...

$(LINTER):
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | bash -s $(GOLANGCI_VERSION)

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=<(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $$2 $$5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)

echo-release-notes:
	@cat $(RELEASE_NOTES)

RELEASE_CMD=VERSION=$(GORELEASER_VERSION) curl -sL https://git.io/goreleaser | bash -s -- --rm-dist --release-notes $(RELEASE_NOTES)

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
