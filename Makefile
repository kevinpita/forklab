VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/kevinpita/forklab/internal/cli.version=$(VERSION)
GO_FILES = $(shell find . -path './.*' -prune -o -name '*.go' -print)

.PHONY: build test test-race lint fmt fmt-check

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/forklab ./cmd/forklab

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofumpt -w $(GO_FILES)

fmt-check:
	@out="$$(gofumpt -l $(GO_FILES))"; if [ -n "$$out" ]; then echo "not gofumpt-formatted:"; echo "$$out"; exit 1; fi
