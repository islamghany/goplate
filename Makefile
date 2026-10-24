# goplate developer tasks (ADR-0017). Run `make help` for the list.
#
# Local configuration comes from .env (copy .env.example). Nothing secret is
# written in this file.

-include .env
export

GO            ?= go
BIN           := $(CURDIR)/bin
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS       := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Pinned tool versions: bump deliberately, in their own commit.
SQLC_VERSION          := v1.31.1
GOLANGCI_LINT_VERSION := v2.14.0

.DEFAULT_GOAL := help

## help: list targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## //' | column -t -s ':'

## up: start Postgres, Redis, Jaeger and Prometheus
.PHONY: up
up:
	docker compose up -d --wait

## down: stop the local stack (keeps data; `docker compose down -v` wipes it)
.PHONY: down
down:
	docker compose down

## run: run the API locally against the compose stack
.PHONY: run
run:
	$(GO) run ./cmd/api

## migrate: apply pending database migrations
.PHONY: migrate
migrate:
	$(GO) run ./cmd/admin migrate up

## migrate-down: roll back the latest migration
.PHONY: migrate-down
migrate-down:
	$(GO) run ./cmd/admin migrate down

## migrate-status: show migration state
.PHONY: migrate-status
migrate-status:
	$(GO) run ./cmd/admin migrate status

## migration: create a new migration, e.g. make migration name=add_projects
.PHONY: migration
migration:
	@test -n "$(name)" || (echo "usage: make migration name=<name>" && exit 1)
	$(GO) run ./cmd/admin migrate create $(name)

## seed: create the development admin user
.PHONY: seed
seed:
	$(GO) run ./cmd/admin seed

## generate: regenerate sqlc code from SQL
.PHONY: generate
generate: $(BIN)/sqlc
	$(BIN)/sqlc generate

## lint: run golangci-lint (includes the architecture rules from ADR-0002)
.PHONY: lint
lint: $(BIN)/golangci-lint
	$(BIN)/golangci-lint run ./...

## test: run unit tests with the race detector
.PHONY: test
test:
	$(GO) test -race -count=1 ./...

## test-integration: run integration tests against real Postgres/Redis containers
.PHONY: test-integration
test-integration:
	$(GO) test -race -count=1 -tags=integration ./...

## cover: unit + integration coverage report (coverage.html)
.PHONY: cover
cover:
	$(GO) test -count=1 -tags=integration -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

## check: everything CI would run
.PHONY: check
check: lint test test-integration
	@git diff --exit-code -- '*.sql.go' '*sqlc_*.go' || (echo "generated code is stale: run make generate" && exit 1)

## build: build both binaries into ./bin
.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/api ./cmd/api
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/admin ./cmd/admin

## docker-build: build the container image
.PHONY: docker-build
docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t goplate:$(VERSION) .

## tools: install pinned sqlc and golangci-lint into ./bin
.PHONY: tools
tools: $(BIN)/sqlc $(BIN)/golangci-lint

# Tools are built with this module's toolchain (go.mod `toolchain`), so the
# linter understands every language feature the code uses.
TOOLCHAIN := $(shell $(GO) env GOVERSION)

$(BIN)/sqlc:
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

$(BIN)/golangci-lint:
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
