BINARY=wp-cloner
VERSION=0.1.0

.PHONY: build run test clean deps

build:
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/

# Сборка под Linux сервер (если разрабатываешь на Mac)
build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY)-linux ./cmd/

deps:
	go mod tidy

test-conn:
	./$(BINARY) -test

dry-run:
	./$(BINARY) -dry-run

run:
	./$(BINARY)

clean:
	rm -f $(BINARY) $(BINARY)-linux
