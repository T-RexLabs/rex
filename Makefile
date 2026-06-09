.PHONY: help all build build-central install install-central test test-race test-pg pg-up pg-down web-dev web-dev-down lint lint-strict vet fmt tidy clean

GO       ?= go
BIN_DIR  ?= bin

# PKGS is the full module (local + the parked central node under
# rex-centralized/). LOCAL_PKGS is the default dev surface: the local
# binary and shared core only. The central node lives in rex-centralized/
# (git-ignored while it is being broken out) and is opt-in via the
# *-central targets, test-pg, and web-dev. Keeping it out of the default
# build/test/vet keeps the local experience buildable even if the parked
# central tree is absent or mid-surgery.
PKGS       := ./...
LOCAL_PKGS := ./cmd/rex/... ./internal/...
CENTRAL_CMD := ./rex-centralized/cmd/rex-central

# `make` with no target prints help; run `make all` for vet+lint+test+build.
.DEFAULT_GOAL := help

all: vet lint test build ## Run vet, lint, tests, and build

build: ## Build the local rex binary into $(BIN_DIR)
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/rex ./cmd/rex

build-central: ## Build the parked central node (requires rex-centralized/)
	@if [ ! -d rex-centralized/cmd/rex-central ]; then \
		echo "build-central: rex-centralized/ not present (central node is parked + git-ignored)"; exit 1; fi
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/rex-central $(CENTRAL_CMD)

install: ## Install the local rex binary into $$GOBIN (or $$GOPATH/bin)
	$(GO) install ./cmd/rex

install-central: ## Install the parked central node (requires rex-centralized/)
	$(GO) install $(CENTRAL_CMD)

test: ## Run local tests (cmd/rex + shared core; excludes parked central)
	$(GO) test $(LOCAL_PKGS)

test-race: ## Run local tests with the race detector
	$(GO) test -race $(LOCAL_PKGS)

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

# web-dev brings up a local rex-central with everything wired
# (pg, web UI, dev identity registered as admin) so a developer
# can `open http://127.0.0.1:8080/login` and click around.
# Separate container + DB from the test pg so a `make test-pg`
# run never collides with `make web-dev`.
WEB_DEV_PG_DSN ?= postgres://postgres:dev@127.0.0.1:55433/rex_dev?sslmode=disable
WEB_DEV_ADDR   ?= 127.0.0.1:8080
web-dev: build build-central ## Start rex-central --dev (pg + web + auto-admin) — open http://127.0.0.1:8080/login
	@docker rm -f rex-pg-dev >/dev/null 2>&1 || true
	docker run -d --name rex-pg-dev \
		-e POSTGRES_PASSWORD=dev -e POSTGRES_DB=rex_dev \
		-p 55433:5432 postgres:17-alpine >/dev/null
	@printf 'rex-pg-dev started; waiting for readiness'
	@until docker exec rex-pg-dev pg_isready -U postgres >/dev/null 2>&1; do printf '.'; sleep 1; done
	@echo ' ready.'
	@echo
	@echo '  In another terminal, sign in (sets the rex_session cookie in your default browser):'
	@echo '    $(BIN_DIR)/rex remote login dev http://$(WEB_DEV_ADDR)'
	@echo '  Then click around starting at http://$(WEB_DEV_ADDR)/orgs/default/members'
	@echo '  (Ctrl-C here tears down rex-central; run `make web-dev-down` to drop the pg container.)'
	@echo
	$(BIN_DIR)/rex-central serve --dev --db '$(WEB_DEV_PG_DSN)' --addr $(WEB_DEV_ADDR) --log-format text

web-dev-down: ## Stop and remove the web-dev Postgres container
	docker rm -f rex-pg-dev >/dev/null 2>&1 && echo "rex-pg-dev removed" || echo "rex-pg-dev not running"

vet: ## Run go vet on the local surface
	$(GO) vet $(LOCAL_PKGS)

fmt: ## Format Go sources (local surface)
	$(GO) fmt $(LOCAL_PKGS)

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
