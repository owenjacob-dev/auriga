# Auriga build targets. See CONTRIBUTING.md.

GO        ?= go
BIN       := bin/auriga
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
GOBIN     := $(shell $(GO) env GOPATH)/bin
GOLANGCI  ?= $(GOBIN)/golangci-lint
GOVULN    ?= $(GOBIN)/govulncheck
COMPOSE   ?= docker compose -f test/integration/compose.yaml
FUZZTIME  ?= 10s

.PHONY: all build lint test test-secrets itest vuln fuzz cover clean

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/auriga

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GOLANGCI) config verify
	$(GOLANGCI) run ./...

test:
	$(GO) test -race ./...

# The secrets-leak suite. Run before every commit.
test-secrets:
	$(GO) test -race -count=1 ./test/secrets/...

# Docker Compose end-to-end suite on a private network passphrase.
itest:
	test/integration/setup.sh
	$(COMPOSE) up -d --build --wait
	$(GO) test -race -count=1 -tags integration ./test/integration/... ./internal/remote/... ; status=$$?; \
	$(COMPOSE) down -v; exit $$status

vuln:
	$(GOVULN) ./...

# Runs every fuzz target for FUZZTIME each (go test fuzzes one at a time).
# A failing input is saved under the package's testdata/fuzz; commit it as a
# regression case with the fix.
fuzz:
	@set -e; for pkg in $$($(GO) list ./...); do \
		for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			echo "== $$pkg $$f"; \
			$(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

cover:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

clean:
	$(GO) clean
	rm -f $(BIN) coverage.out
