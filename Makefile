# Default flags used by the test, testci, testcover targets
COVERAGE_PATH ?= coverage.out
COVERAGE_ARGS ?= -covermode=atomic -coverprofile=$(COVERAGE_PATH)
TEST_ARGS     ?= -race -count=1 -timeout=5s
CI_TEST_ARGS  ?= -timeout=60s
AUTOBAHN_ARGS ?= -race -count=1 -timeout=120s
BENCH_COUNT   ?= 10
BENCH_ARGS    ?= -bench=. -benchmem -count=$(BENCH_COUNT) -run=^$$
DOCS_PORT     ?= :8080

# 3rd party tools
CMD_GOFUMPT     := go run mvdan.cc/gofumpt@v0.8.0
CMD_PKGSITE     := go run golang.org/x/pkgsite/cmd/pkgsite@latest
CMD_REVIVE      := go run github.com/mgechev/revive@v1.9.0
CMD_STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@2025.1.1

# Find examples to build
OUT_DIR         ?= out
OUT_DIR_ABS     := $(abspath $(OUT_DIR))
EXAMPLE_DIR     := examples
EXAMPLE_NAMES   := $(shell ls $(EXAMPLE_DIR))
EXAMPLE_PATHS   := $(addprefix $(OUT_DIR)/,$(EXAMPLE_NAMES)) # -> $(OUT_DIR)/foo $(OUT_DIR)/bar


# ===========================================================================
# Tests
# ===========================================================================
test:
	go test $(TEST_ARGS) ./...
.PHONY: test

# Test command to run for continuous integration, which includes code coverage
# based on codecov.io's documentation:
# https://github.com/codecov/example-go/blob/b85638743b972bd0bd2af63421fe513c6f968930/README.md
testci:
	go test $(TEST_ARGS) $(CI_TEST_ARGS) $(COVERAGE_ARGS) ./...
.PHONY: testci

testcover: testci
	go tool cover -html=$(COVERAGE_PATH)
.PHONY: testcover

# Run the autobahn fuzzingclient test suite (requires docker running locally).
#
# See "Autobahn integration tests" in the README for documentation on how to
# configure these tests.
testautobahn:
	AUTOBAHN=1 go test -run ^TestAutobahn$$ $(AUTOBAHN_ARGS) ./...
.PHONY: testautobahn


# ===========================================================================
# Benchmarks
# ===========================================================================
bench:
	go test $(BENCH_ARGS)
.PHONY: bench

benchquick:
	@ $(MAKE) bench BENCH_COUNT=2
.PHONY: benchquick

# ===========================================================================
# Linting/formatting
# ===========================================================================
lint:
	test -z "$$($(CMD_GOFUMPT) -d -e .)" || (echo "Error: gofmt failed"; $(CMD_GOFUMPT) -d -e . ; exit 1)
	go vet ./...
	$(CMD_REVIVE) -set_exit_status ./...
	$(CMD_STATICCHECK) ./...
.PHONY: lint

fmt:
	$(CMD_GOFUMPT) -d -e -w .
.PHONY: fmt


docs:
	$(CMD_PKGSITE) -http $(DOCS_PORT)


# ===========================================================================
# Example binaries
# ===========================================================================
# Build every example we found
examples: $(EXAMPLE_PATHS)
.PHONY: examples

# Build a specific example
$(OUT_DIR)/%: force-rebuild
	@mkdir -p $(OUT_DIR)
	@echo "Building $@"
	cd $(EXAMPLE_DIR)/$* && CGO_ENABLED=0 go build $(BUILD_ARGS) -o $(abspath $(OUT_DIR))/$*

# phony target used to always rebuild the example binaries
.PHONY: force-rebuild
force-rebuild:

clean:
	rm -rf $(OUT_DIR) $(COVERAGE_PATH)
.PHONY: clean
