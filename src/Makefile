.PHONY: build test lint install clean

build:
	go build -o bin/olx-pp-cli ./cmd/olx-pp-cli

test:
	go test ./...

lint:
	golangci-lint run

install:
	go install ./cmd/olx-pp-cli

clean:
	rm -rf bin/

build-mcp:
	go build -o bin/olx-pp-mcp ./cmd/olx-pp-mcp

install-mcp:
	go install ./cmd/olx-pp-mcp

build-all: build build-mcp
