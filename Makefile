# Auriga build targets. See CONTRIBUTING.md.

GO        ?= go
BIN       := bin/auriga
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
GOBIN     := $(shell $(GO) env GOPATH)/bin
GOLANGCI  ?= $(GOBIN)/golangci-lint
GOVULN    ?= $(GOBIN)/govulncheck
COMPOSE   ?= docker compose -f test/integration/compose.yaml

.PHONY: all build lint test test-secrets itest vuln cover clean

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/auriga

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GOLANGCI) run ./...

test:
	$(GO) test -race ./...

# The secrets-leak suite. Run before every commit.
test-secrets:
	$(GO) test -race -count=1 ./test/secrets/...

# Docker Compose end-to-end suite on a private network passphrase.
itest:
	$(COMPOSE) up -d --build --wait
	$(GO) test -race -count=1 -tags integration ./test/integration/... ; status=$$?; \
	$(COMPOSE) down -v; exit $$status

vuln:
	$(GOVULN) ./...

cover:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

clean:
	$(GO) clean
	rm -f $(BIN) coverage.out
