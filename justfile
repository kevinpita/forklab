version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-X github.com/kevinpita/forklab/internal/cli.version=" + version
go_files := "find . -path './.*' -prune -o -name '*.go' -print"

default:
    @just --list

build:
    go build -trimpath -ldflags "{{ldflags}}" -o bin/forklab ./cmd/forklab

run *args: build
    ./bin/forklab {{args}}

test:
    go test -race ./...

lint:
    golangci-lint run ./...

fmt:
    gofumpt -w $({{go_files}})

check: fmt lint test build

clean:
    rm -rf bin
