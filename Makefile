# Determine root directory
ROOT_DIR=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

# Gather all .go files for use in dependencies below
GO_FILES=$(shell find $(ROOT_DIR) -name '*.go')

# Gather list of expected binaries
BINARIES=txtop

# Set version strings based on git tag and current ref
GO_LDFLAGS=-ldflags "-s -w -X 'main.Version=$(shell git describe --tags --exact-match 2>/dev/null)' -X 'main.CommitHash=$(shell git rev-parse --short HEAD)'"

.PHONY: build mod-tidy clean test

all: format build

# Alias for building program binary
build: $(BINARIES)

tidy:
	# Needed to fetch new dependencies and add them to go.mod
	go mod tidy

clean:
	rm -f $(BINARIES)

format: tidy
	go fmt ./...

test: tidy
	go test -v ./...

# Build our program binaries
# Depends on GO_FILES to determine when rebuild is needed
$(BINARIES): mod-tidy $(GO_FILES)
	CGO_ENABLED=0 go build \
		$(GO_LDFLAGS) \
		-o $(@) .
