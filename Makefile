.PHONY: help all build install test test-race test-pg pg-up pg-down lint lint-strict vet fmt tidy clean

GO       ?= go
BIN_DIR  ?= bin
PKGS     := ./...

# `make` with no target prints help; run `make all` for vet+lint+test+build.
.DEFAULT_GOAL := help

all: vet lint test build ## Run vet, lint, tests, and build

build: ## Build all binaries into $(BIN_DIR)
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/rex ./cmd/rex
	$(GO) build -o $(BIN_DIR)/rex-central ./cmd/rex-central

install: ## Install binaries into $$GOBIN (or $$GOPATH/bin) so they are on PATH
	$(GO) install ./cmd/rex ./cmd/rex-central

test: ## Run all tests
	$(GO) test $(PKGS)

test-race: ## Run tests with the race detector
	$(GO) test -race $(PKGS)

# Postgres-backed central tests. Local devs run `make pg-up` once
# to start a postgres container, then `make test-pg` whenever they
# want the full suite (including the central PostgresStore tests
# that skip without REX_PG_TEST_DSN).
PG_DSN ?= postgres://postgres:dev@127.0.0.1:55432/rex_test?sslmode=disable
test-pg: ## Run the test suite with REX_PG_TEST_DSN set (requires `make pg-up`)
	REX_PG_TEST_DSN='$(PG_DSN)' $(GO) test -race $(PKGS)

pg-up: ## Start a local Postgres container for test-pg / rex-central --db
	docker rm -f rex-pg-test >/dev/null 2>&1 || true
	docker run -d --name rex-pg-test \
		-e POSTGRES_PASSWORD=dev -e POSTGRES_DB=rex_test \
		-p 55432:5432 postgres:17-alpine >/dev/null
	@echo 'rex-pg-test started; DSN=$(PG_DSN)'

pg-down: ## Stop and remove the local Postgres container
	docker rm -f rex-pg-test >/dev/null 2>&1 && echo "rex-pg-test removed" || echo "rex-pg-test not running"

vet: ## Run go vet
	$(GO) vet $(PKGS)

fmt: ## Format Go sources
	$(GO) fmt $(PKGS)

tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

lint: ## Run golangci-lint if installed; skip with install hint otherwise
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "lint: golangci-lint not installed; skipping (install: 'brew install golangci-lint' or 'go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest'). Run 'make lint-strict' to fail instead."; \
	fi

lint-strict: ## Run golangci-lint and fail if it is not installed (CI semantics)
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.txt coverage.html

help: ## Show this help
	@echo "Usage: make <target>"
	@echo
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
