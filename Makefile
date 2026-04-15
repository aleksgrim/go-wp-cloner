BINARY  = wp-cloner
VERSION = 0.3.0

.PHONY: build build-linux build-darwin vet deps test-conn dry-run run remove remove-force clean

# Build binary for the current platform
build:
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/

# Build binary for Linux amd64 (for server deployment)
build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY)-linux-amd64 ./cmd/

# Build binary for macOS arm64 (Apple Silicon)
build-darwin:
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY)-darwin-arm64 ./cmd/

# Tidy go.mod and go.sum
deps:
	go mod tidy

# Run static analysis
vet:
	go vet ./...

# Test SSH connection and verify required tools on the server
test-conn:
	go run ./cmd/ -test

# Show cloning plan without executing
dry-run:
	go run ./cmd/ -dry-run

# Clone sites listed in domains.txt
run:
	go run ./cmd/

# Remove sites listed in domains.txt (prompts for confirmation)
remove:
	go run ./cmd/ -remove

# Remove sites without confirmation prompt (for scripting)
remove-force:
	go run ./cmd/ -remove -force

# Delete compiled binaries
clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-darwin-arm64