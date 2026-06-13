.PHONY: build test lint install clean build-mcp install-mcp build-all build-linux release

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

# Cross-compile both binaries for a Linux/amd64 VPS from any dev host.
# Outputs bin/olx-pp-cli-linux and bin/olx-pp-mcp-linux for scp deploy.
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/olx-pp-cli-linux ./cmd/olx-pp-cli
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/olx-pp-mcp-linux ./cmd/olx-pp-mcp

# Full release artifact set for a new VPS: test, cross-build, checksums.
release: test build-linux
	cd bin && sha256sum olx-pp-cli-linux olx-pp-mcp-linux > SHA256SUMS.txt
	@echo "release artifacts in src/bin/: olx-pp-cli-linux, olx-pp-mcp-linux, SHA256SUMS.txt"
