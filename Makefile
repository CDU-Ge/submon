BINARY  := submon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX  := $(HOME)/.local

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT)

.PHONY: all build install test vet fmt clean

all: build

## build: compile the binary into ./submon
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

## install: build and install into ~/.local/bin
install: build
	install -Dm755 $(BINARY) $(PREFIX)/bin/$(BINARY)

## test: run unit tests
test:
	go test ./...

## vet + fmt: static checks and formatting
vet:
	go vet ./...

fmt:
	gofmt -w .
	go mod tidy

## clean: remove build artifacts
clean:
	rm -f $(BINARY)

## release: cross-compile linux/amd64 + darwin/{amd64,arm64} into dist/
RELEASE_DIRS := linux_amd64 darwin_amd64 darwin_arm64
release: $(RELEASE_DIRS:%=dist/$(BINARY)-%)

dist/$(BINARY)-linux_amd64:
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_amd64:
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_arm64:
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

.PHONY: release
