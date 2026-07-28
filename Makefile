.PHONY: build build-lite build-windows test test-race lint install install-lite clean

BINARY=snip
BUILD_DIR=cmd/snip
VERSION=$(shell git describe --tags --always 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS=-ldflags="-s -w -X 'github.com/edouard-claude/snip/internal/cli.version=$(VERSION)'"
GOLANGCI_LINT_VERSION=$(shell cat .golangci-lint-version)

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(LDFLAGS) ./$(BUILD_DIR)

build-lite:
	CGO_ENABLED=0 go build -tags lite -o $(BINARY) $(LDFLAGS) ./$(BUILD_DIR)

build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY).exe $(LDFLAGS) ./$(BUILD_DIR)

test:
	go test -cover ./...

test-race:
	go test -race -cover ./...

vet:
	go vet ./...

# Filter YAML tests are not exercised by go test.
verify:
	go run ./cmd/snip verify

# Reuse the required version when installed; otherwise run it from the Go cache.
lint: vet
	@if command -v golangci-lint > /dev/null 2>&1 \
		&& [ "v$$(golangci-lint version --short)" = "$(GOLANGCI_LINT_VERSION)" ]; then \
		golangci-lint run; \
	else \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run; \
	fi

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: ci vet verify vulncheck
ci: test-race verify lint vulncheck

install: build
	cp $(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || cp $(BINARY) /usr/local/bin/$(BINARY)

install-lite: build-lite
	cp $(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || cp $(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY) $(BINARY).exe
	go clean -testcache
