BINARY  := submon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX  := $(HOME)/.local

# Windows 上可执行文件必须带 .exe 后缀才能直接双击 / 按名字运行。
EXE := $(if $(filter windows,$(shell go env GOOS)),.exe,)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT)

.PHONY: all build build-statusline install test vet fmt clean

all: build

## build: compile the binary into ./submon(Windows 上自动带 .exe)
build: $(if $(filter windows,$(shell go env GOOS)),build-statusline)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY)$(EXE) .

# The helper is a separate GUI-subsystem app; the main CLI stays a console app.
STATUSLINE_ARCH ?= $(shell go env GOARCH)
build-statusline:
	GOOS=windows GOARCH=$(STATUSLINE_ARCH) go build -buildvcs=false -trimpath -ldflags "-s -w -H windowsgui" -o internal/statuslinebundle/assets/submon.exe ./apps/statusline

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
	rm -f $(BINARY) $(BINARY).exe internal/statuslinebundle/assets/submon.exe

## release: cross-compile linux/amd64 + darwin/{amd64,arm64} + windows/amd64 into dist/
RELEASE_DIRS := linux_amd64 darwin_amd64 darwin_arm64 windows_amd64
release: $(RELEASE_DIRS:%=dist/$(BINARY)-%)

dist/$(BINARY)-linux_amd64:
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_amd64:
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-darwin_arm64:
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .
dist/$(BINARY)-windows_amd64: STATUSLINE_ARCH=amd64
dist/$(BINARY)-windows_amd64: build-statusline
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $@.exe .

.PHONY: release

deploy-%: install
	rsync -avz --progress ~/.local/bin/submon $*:~/.local/bin/submon
