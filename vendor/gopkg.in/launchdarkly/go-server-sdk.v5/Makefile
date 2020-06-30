
GOLANGCI_LINT_VERSION=v1.27.0

LINTER=./bin/golangci-lint
LINTER_VERSION_FILE=./bin/.golangci-lint-version-$(GOLANGCI_LINT_VERSION)

TEST_BINARY=./go-server-sdk.test

OUTPUT_DIR=./build

ALL_SOURCES := $(shell find * -type f -name "*.go")

COVERAGE_PROFILE_RAW=./build/coverage_raw.out
COVERAGE_PROFILE_RAW_HTML=./build/coverage_raw.html
COVERAGE_PROFILE_FILTERED=./build/coverage.out
COVERAGE_PROFILE_FILTERED_HTML=./build/coverage.html
COVERAGE_ENFORCER_FLAGS=-package gopkg.in/launchdarkly/go-server-sdk.v5 -skipfiles sharedtest/ -skipcode "// COVERAGE" -packagestats -filestats -showcode

.PHONY: build clean test test-coverage benchmarks benchmark-allocs lint

build:
	go build ./...

clean:
	go clean

test:
	go test -race -v ./...
	@# The proxy tests must be run separately because Go caches the global proxy environment variables. We use
	@# build tags to isolate these tests from the main test run so that if you do "go test ./..." you won't
	@# get unexpected errors.
	for tag in proxytest1 proxytest2; do go test -race -v -tags=$$tag ./proxytest; done

test-coverage: $(COVERAGE_PROFILE_RAW)
	if [ -z "$(which go-coverage-enforcer)" ]; then go get github.com/launchdarkly-labs/go-coverage-enforcer; fi
	go-coverage-enforcer $(COVERAGE_ENFORCER_FLAGS) -outprofile $(COVERAGE_PROFILE_FILTERED) $(COVERAGE_PROFILE_RAW) || true  # "|| true" so the build won't fail now while we're still improving coverage
	go tool cover -html $(COVERAGE_PROFILE_FILTERED) -o $(COVERAGE_PROFILE_FILTERED_HTML)
	go tool cover -html $(COVERAGE_PROFILE_RAW) -o $(COVERAGE_PROFILE_RAW_HTML)

$(COVERAGE_PROFILE_RAW): $(ALL_SOURCES)
	@mkdir -p ./build
	go test -coverprofile $(COVERAGE_PROFILE_RAW) -coverpkg=./... ./... >/dev/null
	# note that -coverpkg=./... is necessary so it aggregates coverage to include inter-package references

benchmarks:
	go test -benchmem '-run=^$$' gopkg.in/launchdarkly/go-server-sdk.v5 -bench .

# See CONTRIBUTING.md regarding the use of the benchmark-allocs target. Notes about this implementation:
# 1. We precompile the test code because otherwise the allocation traces will include the actions of the compiler itself.
# 2. "benchtime=3x" means the run count (b.N) is set to 3. Setting it to 1 would produce less redundant output, but the
#    benchmark statistics seem to be less reliable if the run count is less than 3 - they will report a higher allocation
#    count per run, possibly because the first run
benchmark-allocs:
	@if [ -z "$$BENCHMARK" ]; then echo "must specify BENCHMARK=" && exit 1; fi
	@mkdir -p $(OUTPUT_DIR)
	@echo Precompiling test code to $(TEST_BINARY)
	@go test -c -o $(TEST_BINARY) >/dev/null 2>&1
	@echo "Generating heap allocation traces in $(ALLOCATIONS_LOG) for benchmark(s): $$BENCHMARK"
	@echo "You should see some benchmark result output; if you do not, you may have misspelled the benchmark name/regex"
	@GODEBUG=allocfreetrace=1 LD_TEST_ALLOCATIONS= $(TEST_BINARY) -test.run=none -test.bench=$$BENCHMARK -test.benchmem -test.benchtime=1x 2>$(ALLOCATIONS_LOG)

$(LINTER_VERSION_FILE):
	rm -f $(LINTER)
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | bash -s $(GOLANGCI_LINT_VERSION)
	touch $(LINTER_VERSION_FILE)

lint: $(LINTER_VERSION_FILE)
	$(LINTER) run ./...
