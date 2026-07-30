.PHONY: build build-lite build-windows test test-install test-race lint install install-lite upgrade upgrade-lite clean

BINARY=snip
BUILD_DIR=cmd/snip
VERSION=$(shell git describe --tags --always 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS=-ldflags="-s -w -X 'github.com/edouard-claude/snip/internal/cli.version=$(VERSION)'"

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(LDFLAGS) ./$(BUILD_DIR)

build-lite:
	CGO_ENABLED=0 go build -tags lite -o $(BINARY) $(LDFLAGS) ./$(BUILD_DIR)

build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY).exe $(LDFLAGS) ./$(BUILD_DIR)

test:
	go test -cover ./...

test-install:
	sh tests/source-install/test.sh

test-race:
	go test -race ./...

lint:
	go vet ./...
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

install: build

install-lite: build-lite

upgrade: build

upgrade-lite: build-lite

install install-lite upgrade upgrade-lite:
	@case "$@" in \
		upgrade*) destination="$$(scripts/install-path --upgrade)" ;; \
		*) destination="$$(scripts/install-path)" ;; \
	esac || exit 1; \
	install_dir="$$(dirname "$$destination")"; \
	printf 'Installing %s\n' "$$destination"; \
	if ! mkdir -p "$$install_dir"; then \
		printf 'snip: cannot create install directory %s; set GOBIN to a writable directory\n' "$$install_dir" >&2; \
		exit 1; \
	fi; \
	temporary="$$(mktemp "$$install_dir/.snip.install.XXXXXX")" || { \
		printf 'snip: cannot create temporary file in %s; set GOBIN to a writable directory\n' "$$install_dir" >&2; \
		exit 1; \
	}; \
	trap 'rm -f "$$temporary"' 0; \
	trap 'exit 1' 1 2 15; \
	if ! cp "$(BINARY)" "$$temporary"; then \
		printf 'snip: cannot install to %s; set GOBIN to a writable directory\n' "$$destination" >&2; \
		exit 1; \
	fi; \
	if ! chmod 0755 "$$temporary"; then \
		printf 'snip: cannot make %s executable; set GOBIN to a writable directory\n' "$$destination" >&2; \
		exit 1; \
	fi; \
	if ! mv -f "$$temporary" "$$destination"; then \
		printf 'snip: cannot replace %s; set GOBIN to a writable directory\n' "$$destination" >&2; \
		exit 1; \
	fi; \
	trap - 0 1 2 15

clean:
	rm -f $(BINARY) $(BINARY).exe
	go clean -testcache
