# Determine root directory
ROOT_DIR=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

# Gather all .go files for use in dependencies below
GO_FILES=$(shell find $(ROOT_DIR) -name '*.go')

# Gather list of expected binaries
BINARIES=txtop
GOOS ?= $(shell go env GOOS)
BINARY_SUFFIX := $(if $(filter windows,$(GOOS)),.exe,)
BINARY_OUTPUTS := $(addsuffix $(BINARY_SUFFIX),$(BINARIES))

# Set version strings based on git tag and current ref
GO_LDFLAGS=-ldflags "-s -w -X 'main.Version=$(shell git describe --tags --exact-match 2>/dev/null)' -X 'main.CommitHash=$(shell git rev-parse --short HEAD)'"

.PHONY: build mod-tidy clean test

all: format build

# Alias for building program binary
build: $(BINARY_OUTPUTS)

mod-tidy:
	# Needed to fetch new dependencies and add them to go.mod
	go mod tidy

clean:
	rm -f $(BINARY_OUTPUTS)

format: mod-tidy
	go fmt ./...
	gofmt -s -w $(GO_FILES)

golines:
	golines -w --ignore-generated --chain-split-dots --max-len=80 --reformat-tags .

test: mod-tidy
	go test -v -race ./...

# Build our program binaries
# Depends on GO_FILES to determine when rebuild is needed
$(BINARY_OUTPUTS): mod-tidy $(GO_FILES)
	CGO_ENABLED=0 go build \
		$(GO_LDFLAGS) \
		-o $(@) .
