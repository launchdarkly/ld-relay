GOLANGCI_VERSION=v1.7

SHELL=/bin/bash

test: lint
	go test ./...

lint:
	./bin/golangci-lint run ./...

init:
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | bash -s $(GOLANGCI_VERSION)

ifneq ($(shell uname -s),Darwin)  # Mac OS X
COMM_OPTIONS=--nocheck-order
endif

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=<(GIT_EXTERNAL_DIFF='bash -c "comm $(COMM_OPTIONS) -13 $$2 $$5"' git log --ext-diff -1 --pretty= -p CHANGELOG.md | tail -n +3)

echo-release-notes:
	@cat $(RELEASE_NOTES)

release:
	curl -sL https://git.io/goreleaser | bash -s -- --rm-dist --release-notes $(RELEASE_NOTES)

test-release:
	curl -sL https://git.io/goreleaser | bash -s -- --rm-dist --release-notes $(RELEASE_NOTES) --skip-publish --skip-validate

DOCKER_COMPOSE_TEST=docker-compose -f docker-compose.test.yml

test-centos test-debian test-docker: test-release
	$(DOCKER_COMPOSE_TEST) up --force-recreate -d $(subst test,relay,$@)
	trap "$(DOCKER_COMPOSE_TEST) rm -f" EXIT; $(DOCKER_COMPOSE_TEST) run --rm $@

test-all: test-centos test-debian test-docker

.PHONY: docker init lint release test test-release test-centos test-debian test-docker test-all
