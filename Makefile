.PHONY: build test lint fmt vet tidy clean help proto-gen proto-lint \
       docker-build docker-build-controller docker-build-engine-claude-code docker-build-engine-codex \
       helm-lint

BINARY := bin/robodev
GO := go
GOFLAGS := -v
REGISTRY ?= ghcr.io/robodev-inc
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the controller binary
	$(GO) build $(GOFLAGS) -o $(BINARY) ./cmd/robodev/

test: ## Run all unit tests
	$(GO) test $(GOFLAGS) ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format code with gofumpt
	gofumpt -w .

vet: ## Run go vet
	$(GO) vet ./...

tidy: ## Run go mod tidy
	$(GO) mod tidy

proto-lint: ## Lint protobuf definitions with buf
	buf lint

proto-gen: ## Generate Go code from protobuf definitions
	buf generate

clean: ## Remove build artefacts
	rm -rf bin/

docker-build-controller: ## Build controller container image
	docker build -t $(REGISTRY)/robodev:$(VERSION) -f docker/controller/Dockerfile .

docker-build-engine-claude-code: ## Build Claude Code engine container image
	docker build -t $(REGISTRY)/engine-claude-code:$(VERSION) -f docker/engine-claude-code/Dockerfile docker/engine-claude-code/

docker-build-engine-codex: ## Build Codex engine container image
	docker build -t $(REGISTRY)/engine-codex:$(VERSION) -f docker/engine-codex/Dockerfile docker/engine-codex/

docker-build: docker-build-controller docker-build-engine-claude-code docker-build-engine-codex ## Build all container images

helm-lint: ## Lint the Helm chart
	helm lint charts/robodev/

.DEFAULT_GOAL := help
