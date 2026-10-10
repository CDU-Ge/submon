BINARY  := submon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX  := $(HOME)/.local

# Windows 上可执行文件必须带 .exe 后缀才能直接双击 / 按名字运行。
EXE := $(if $(filter windows,$(shell go env GOOS)),.exe,)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT)

.PHONY: all build install test vet fmt clean

all: build

## build: compile the binary into ./submon(Windows 上自动带 .exe)
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY)$(EXE) .

## install: build and install into ~/.local/bin(POSIX;Windows 请用 submon install)
install: build
	install -Dm755 $(BINARY)$(EXE) $(PREFIX)/bin/$(BINARY)$(EXE)

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
	rm -f $(BINARY) $(BINARY).exe

## release: cross-compile linux/amd64 + darwin/{amd64,arm64} + windows/amd64 into dist/
RELEASE_DIRS := linux_amd64 darwin_amd64 darwin_arm64 windows_amd64
release: $(RELEASE_DIRS:%=dist/$(BINARY)-%)

dist/$(BINARY)-linux_amd64:
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_amd64:
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_arm64:
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-windows_amd64:
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@.exe .

.PHONY: release

deploy-%: install
	rsync -avz --progress ~/.local/bin/submon $*:~/.local/bin/submon
