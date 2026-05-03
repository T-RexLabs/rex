.PHONY: help all build test test-race lint vet fmt tidy clean

GO       ?= go
BIN_DIR  ?= bin
PKGS     := ./...

# `make` with no target prints help; run `make all` for lint+test+build.
.DEFAULT_GOAL := help

all: lint test build ## Run lint, tests, and build

build: ## Build all binaries into $(BIN_DIR)
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/rex ./cmd/rex
	$(GO) build -o $(BIN_DIR)/rex-central ./cmd/rex-central

test: ## Run all tests
	$(GO) test $(PKGS)

test-race: ## Run tests with the race detector
	$(GO) test -race $(PKGS)

vet: ## Run go vet
	$(GO) vet $(PKGS)

fmt: ## Format Go sources
	$(GO) fmt $(PKGS)

tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

lint: ## Run golangci-lint (must be installed locally)
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.txt coverage.html

help: ## Show this help
	@echo "Usage: make <target>"
	@echo
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
