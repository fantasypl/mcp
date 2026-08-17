.PHONY: build test lint

build:
	mkdir -p bin
	go build -o bin/fpl-mcp ./cmd/fpl-mcp
	go build -o bin/fplctl ./cmd/fplctl

test:
	go test ./... -race -count=1

lint:
	golangci-lint run
