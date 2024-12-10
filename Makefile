# Default flags used by the test, testci, testcover targets
COVERAGE_PATH ?= coverage.out
COVERAGE_ARGS ?= -covermode=atomic -coverprofile=$(COVERAGE_PATH)
TEST_ARGS     ?= -race -timeout 60s

# 3rd party tools
LINT        := go run github.com/mgechev/revive@v1.5.1
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@2024.1.1

test:
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


lint:
	test -z "$$(gofmt -d -s -e .)" || (echo "Error: gofmt failed"; gofmt -d -s -e . ; exit 1)
	go vet ./...
	$(LINT) -set_exit_status ./...
	$(STATICCHECK) ./...
.PHONY: lint

clean:
	rm -rf $(COVERAGE_PATH)
.PHONY: clean
