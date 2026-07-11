# ai-substation -- developer Makefile
#
# Overridable variables:
#   IMAGE / TAG        image name + tag           (default ai-substation:latest)
#   CONTAINER          container name for up/down  (default ai-substation)
#   PORT               host port for `make up`     (default 8000)
#   PLATFORMS          arches for `make build`     (default linux/amd64,linux/arm64)
#   PUSH               pass PUSH=--push to push multi-arch build to a registry

IMAGE     ?= ai-substation
TAG       ?= latest
CONTAINER ?= ai-substation
PORT      ?= 8000
PLATFORMS ?= linux/amd64,linux/arm64
BUILDER   ?= ai-substation-multiarch
PUSH      ?=

BIN := $(CURDIR)/bin
# Prefer locally-installed tools (./bin) over the system.
export PATH := $(BIN):$(PATH)

.DEFAULT_GOAL := help

.PHONY: help install lint lint-go lint-docker test build up down run clean

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

install: ## Install toolchain + deps to run/lint/test natively
	@command -v go >/dev/null || { echo "Go is required: https://go.dev/dl/"; exit 1; }
	go mod download
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint: present"; \
	elif command -v brew >/dev/null 2>&1; then \
		brew install golangci-lint; \
	else \
		echo "installing golangci-lint into $(BIN)"; \
		mkdir -p $(BIN); \
		GOBIN=$(BIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	@if command -v hadolint >/dev/null 2>&1; then \
		echo "hadolint: present"; \
	elif command -v brew >/dev/null 2>&1; then \
		brew install hadolint; \
	else \
		echo "hadolint: not installed -- 'make lint' will fall back to the hadolint docker image"; \
	fi
	@echo "install complete"

lint: lint-go lint-docker ## Lint everything (go + docker)

lint-go: ## Lint Go sources (gofmt, vet, golangci-lint)
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found -- run 'make install' (ran gofmt + vet only)"; \
	fi

lint-docker: ## Lint the Dockerfile (hadolint)
	@if command -v hadolint >/dev/null 2>&1; then \
		hadolint Dockerfile; \
	elif command -v docker >/dev/null 2>&1; then \
		docker run --rm -i hadolint/hadolint < Dockerfile; \
	else \
		echo "neither hadolint nor docker available -- skipping Dockerfile lint"; \
	fi

test: ## Run the Go test suite
	go test ./... -count=1

build: ## Build the multi-arch (amd64 + arm64) docker image
	@docker buildx version >/dev/null 2>&1 || { echo "docker buildx is required"; exit 1; }
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || \
		docker buildx create --name $(BUILDER) --driver docker-container >/dev/null
	docker buildx build --builder $(BUILDER) --platform $(PLATFORMS) -t $(IMAGE):$(TAG) $(PUSH) .

up: ## Build (native) if needed and run the container in the background
	@test -f .env || { echo "no .env found -- copy .env.example to .env and fill it in"; exit 1; }
	docker build -t $(IMAGE):$(TAG) .
	-docker rm -f $(CONTAINER) 2>/dev/null
	docker run -d --name $(CONTAINER) -p $(PORT):8000 --env-file .env $(IMAGE):$(TAG)
	@echo "up: http://localhost:$(PORT)/health"

down: ## Stop and remove the container
	-docker rm -f $(CONTAINER) 2>/dev/null
	@echo "down"

run: ## Run natively with go (reads .env)
	@test -f .env || { echo "no .env found -- copy .env.example to .env and fill it in"; exit 1; }
	set -a && . ./.env && set +a && go run ./cmd/server

clean: ## Remove local build artifacts
	rm -rf $(BIN) out server
