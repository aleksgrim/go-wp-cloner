BINARY  = wp-cloner
VERSION = 0.2.0

.PHONY: build build-linux build-darwin vet deps test-conn dry-run run clean

build:
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY)-linux-amd64 ./cmd/

build-darwin:
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY)-darwin-arm64 ./cmd/

deps:
	go mod tidy

vet:
	go vet ./...

test-conn:
	go run ./cmd/ -test

dry-run:
	go run ./cmd/ -dry-run

run:
	go run ./cmd/

clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-darwin-arm64
	