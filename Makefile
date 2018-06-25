GOLANGCI_VERSION=v1.7

test: lint
	go test ./...

lint:
	./bin/golangci-lint run ./...
	gometalinter.v2 ./...

init:
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | bash -s $(GOLANGCI_VERSION)
	go get -u gopkg.in/alecthomas/gometalinter.v2
	gometalinter.v2 --install

.PHONY: docker init lint test
