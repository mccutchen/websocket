# Default flags used by the test, testci, testcover targets
COVERAGE_PATH ?= coverage.out
COVERAGE_ARGS ?= -covermode=atomic -coverprofile=$(COVERAGE_PATH)
TEST_ARGS     ?= -race -timeout 60s

# 3rd party tools
CMD_GOFUMPT     := go run mvdan.cc/gofumpt@v0.5.0
CMD_REVIVE      := go run github.com/mgechev/revive@v1.5.1
CMD_STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@2024.1.1

# Find examples to build
EXAMPLE_DIR     := examples
EXAMPLE_NAMES   := $(shell ls $(EXAMPLE_DIR))
OUT_DIR         ?= bin
OUT_PATHS       := $(addprefix $(OUT_DIR)/,$(EXAMPLE_NAMES)) # -> $(OUT_DIR)/foo $(OUT_DIR)/bar
OUT_DIR_ABS     := $(abspath $(OUT_DIR))

# Build every example we found
build: clean $(OUT_PATHS)
.PHONY: build

# Build a specific example
$(OUT_DIR)/%: force-rebuild
	@mkdir -p $(OUT_DIR)
	@echo "Building $@"
	cd $(EXAMPLE_DIR)/$* && CGO_ENABLED=0 go build $(BUILD_ARGS) -o $(abspath $(OUT_DIR))/$*

test: build
	go test $(TEST_ARGS) ./...
.PHONY: test

# Test command to run for continuous integration, which includes code coverage
# based on codecov.io's documentation:
# https://github.com/codecov/example-go/blob/b85638743b972bd0bd2af63421fe513c6f968930/README.md
testci:
	AUTOBAHN_TESTS=1 go test $(TEST_ARGS) $(COVERAGE_ARGS) ./...
.PHONY: testci

testcover: testci
	go tool cover -html=$(COVERAGE_PATH)
.PHONY: testcover

# Run the autobahn fuzzingclient test suite (requires docker running locally).
#
# To run only a subset of autobahn tests, specify them in an AUTOBAHN_CASES env
# var:
#
#     AUTOBAHN_CASES=5.7,6.12.* make testautobahn
testautobahn:
	AUTOBAHN_TESTS=1 AUTOBAHN_OPEN_REPORT=1 go test -run ^TestWebSocketServer$$ $(TEST_ARGS) ./...
.PHONY: autobahntests


bench:
	go test -bench=. -benchmem
.PHONY: bench
	
lint:
	test -z "$$($(CMD_GOFUMPT) -d -e .)" || (echo "Error: gofmt failed"; $(CMD_GOFUMPT) -d -e . ; exit 1)
	go vet ./...
	$(CMD_REVIVE) -set_exit_status ./...
	$(CMD_STATICCHECK) ./...
.PHONY: lint

fmt:
	$(CMD_GOFUMPT) -d -e -w .
.PHONY: fmt

clean:
	rm -rf $(OUT_DIR) $(COVERAGE_PATH)
.PHONY: clean


# phony target used to always rebuild the example binaries
.PHONY: force-rebuild
force-rebuild:
