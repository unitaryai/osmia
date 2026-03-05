.PHONY: build test lint fmt vet tidy clean help proto-gen proto-lint sdk-gen \
       docker-build docker-build-controller docker-build-engine-claude-code docker-build-engine-codex \
       docker-build-engine-opencode docker-build-engine-cline \
       docker-build-dev docker-build-dev-controller docker-build-dev-engine-claude-code docker-build-dev-engine-codex \
       docker-build-dev-engine-opencode docker-build-dev-engine-cline \
       helm-lint \
       check-prereqs kind-create kind-delete kind-load \
       deploy deploy-test undeploy local-up local-down local-redeploy \
       live-up live-redeploy live-deploy setup-secrets \
       e2e-test e2e-workflow-test e2e-workflow-test-verbose e2e-live-test integration-test test-report test-all logs \
       compose-up compose-down \
       docs-serve docs-build \
       fake-agent-image fake-agent-load

BINARY := bin/robodev
GO := go
GOFLAGS := -v
REGISTRY ?= ghcr.io/unitaryai
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Local development settings
KIND_CLUSTER_NAME ?= robodev
FAKE_AGENT_IMAGE  ?= fake-agent:e2e
KIND_CONFIG       ?= hack/kind-config.yaml
HELM_RELEASE      ?= robodev
HELM_NAMESPACE    ?= robodev
DEV_TAG           ?= dev
VALUES_LIVE       ?= hack/values-live.yaml

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

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

proto-gen: ## Generate Go code from protobuf definitions (see also: sdk-gen)
	buf generate

sdk-gen: ## Generate SDK stubs for Python and TypeScript from protobuf definitions
	buf generate

clean: ## Remove build artefacts
	rm -rf bin/

# ---------------------------------------------------------------------------
# Container images (release)
# ---------------------------------------------------------------------------

docker-build-controller: ## Build controller container image
	docker build -t $(REGISTRY)/robodev:$(VERSION) -f docker/controller/Dockerfile .

docker-build-engine-claude-code: ## Build Claude Code engine container image
	docker build -t $(REGISTRY)/engine-claude-code:$(VERSION) -f docker/engine-claude-code/Dockerfile docker/engine-claude-code/

docker-build-engine-codex: ## Build Codex engine container image
	docker build -t $(REGISTRY)/engine-codex:$(VERSION) -f docker/engine-codex/Dockerfile docker/engine-codex/

docker-build-engine-opencode: ## Build OpenCode engine container image
	docker build -t $(REGISTRY)/engine-opencode:$(VERSION) -f docker/engine-opencode/Dockerfile docker/engine-opencode/

docker-build-engine-cline: ## Build Cline engine container image
	docker build -t $(REGISTRY)/engine-cline:$(VERSION) -f docker/engine-cline/Dockerfile docker/engine-cline/

docker-build: docker-build-controller docker-build-engine-claude-code docker-build-engine-codex docker-build-engine-opencode docker-build-engine-cline ## Build all container images

# ---------------------------------------------------------------------------
# Container images (local dev — fixed "dev" tag)
# ---------------------------------------------------------------------------

docker-build-dev-controller:
	docker build -t $(REGISTRY)/robodev:$(DEV_TAG) -f docker/controller/Dockerfile .

docker-build-dev-engine-claude-code:
	docker build -t $(REGISTRY)/engine-claude-code:$(DEV_TAG) -f docker/engine-claude-code/Dockerfile docker/engine-claude-code/

docker-build-dev-engine-codex:
	docker build -t $(REGISTRY)/engine-codex:$(DEV_TAG) -f docker/engine-codex/Dockerfile docker/engine-codex/

docker-build-dev-engine-opencode:
	docker build -t $(REGISTRY)/engine-opencode:$(DEV_TAG) -f docker/engine-opencode/Dockerfile docker/engine-opencode/

docker-build-dev-engine-cline:
	docker build -t $(REGISTRY)/engine-cline:$(DEV_TAG) -f docker/engine-cline/Dockerfile docker/engine-cline/

docker-build-dev: docker-build-dev-controller docker-build-dev-engine-claude-code docker-build-dev-engine-codex docker-build-dev-engine-opencode docker-build-dev-engine-cline ## Build all images with dev tag

# ---------------------------------------------------------------------------
# Helm
# ---------------------------------------------------------------------------

helm-lint: ## Lint the Helm chart
	helm lint charts/robodev/

# ---------------------------------------------------------------------------
# Local development (kind)
# ---------------------------------------------------------------------------

check-prereqs: ## Verify local dev prerequisites are installed
	@echo "Checking prerequisites..."
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is not installed"; exit 1; }
	@command -v kind >/dev/null 2>&1   || { echo "ERROR: kind is not installed"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is not installed"; exit 1; }
	@command -v helm >/dev/null 2>&1   || { echo "ERROR: helm is not installed"; exit 1; }
	@docker info >/dev/null 2>&1       || { echo "ERROR: docker daemon is not running"; exit 1; }
	@echo "All prerequisites satisfied."

kind-create: check-prereqs ## Create a kind cluster for local development
	@if kind get clusters 2>/dev/null | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists."; \
	else \
		kind create cluster --config $(KIND_CONFIG); \
	fi
	@kubectl create namespace $(HELM_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -

kind-delete: ## Delete the kind cluster
	kind delete cluster --name $(KIND_CLUSTER_NAME)

kind-load: ## Load dev images into the kind cluster
	kind load docker-image $(REGISTRY)/robodev:$(DEV_TAG) --name $(KIND_CLUSTER_NAME)
	@if docker image inspect $(REGISTRY)/engine-claude-code:$(DEV_TAG) >/dev/null 2>&1; then \
		kind load docker-image $(REGISTRY)/engine-claude-code:$(DEV_TAG) --name $(KIND_CLUSTER_NAME); \
	fi
	@if docker image inspect $(REGISTRY)/engine-codex:$(DEV_TAG) >/dev/null 2>&1; then \
		kind load docker-image $(REGISTRY)/engine-codex:$(DEV_TAG) --name $(KIND_CLUSTER_NAME); \
	fi

deploy: ## Deploy to kind cluster via Helm
	helm upgrade --install $(HELM_RELEASE) charts/robodev/ \
		--namespace $(HELM_NAMESPACE) \
		-f charts/robodev/values.yaml \
		-f hack/values-dev.yaml \
		--wait --timeout 120s

undeploy: ## Remove the Helm release
	helm uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE) || true

local-up: build docker-build-dev-controller kind-create kind-load deploy ## Full local setup: build, create cluster, deploy
	@echo ""
	@echo "RoboDev is running. Useful commands:"
	@echo "  make logs            — stream controller logs"
	@echo "  make e2e-test        — run end-to-end tests"
	@echo "  make local-redeploy  — rebuild and redeploy (reuses cluster)"
	@echo "  make local-down      — tear everything down"

local-down: undeploy kind-delete ## Tear down: uninstall release and delete cluster

local-redeploy: build docker-build-dev-controller kind-load deploy ## Fast rebuild and redeploy (reuses existing cluster)

deploy-test: ## Deploy to kind cluster with test values overlay
	helm upgrade --install $(HELM_RELEASE) charts/robodev/ \
		--namespace $(HELM_NAMESPACE) \
		-f charts/robodev/values.yaml \
		-f hack/values-dev.yaml \
		-f hack/values-test.yaml \
		--wait --timeout 120s

fake-agent-image: ## Build the fake-agent container image for E2E workflow tests
	docker build -t $(FAKE_AGENT_IMAGE) hack/fake-agent/

fake-agent-load: fake-agent-image ## Build and load the fake-agent image into the kind cluster
	kind load docker-image $(FAKE_AGENT_IMAGE) --name $(KIND_CLUSTER_NAME)

e2e-workflow-test: ## Run E2E workflow pipeline tests (requires kind cluster + fake-agent-load)
	@kubectl config use-context kind-$(KIND_CLUSTER_NAME) >/dev/null 2>&1 || true
	FAKE_AGENT_IMAGE=$(FAKE_AGENT_IMAGE) \
	$(GO) test -tags=e2e -count=1 -timeout=600s ./tests/e2e/ -run TestWorkflow

e2e-workflow-test-verbose: ## Run E2E workflow pipeline tests with verbose logging (for debugging)
	@kubectl config use-context kind-$(KIND_CLUSTER_NAME) >/dev/null 2>&1 || true
	FAKE_AGENT_IMAGE=$(FAKE_AGENT_IMAGE) \
	$(GO) test -tags=e2e -count=1 -v -timeout=600s ./tests/e2e/ -run TestWorkflow

e2e-live-test: ## Run live E2E tests against the running controller (requires kind-robodev + live secrets)
	@kubectl config use-context kind-$(KIND_CLUSTER_NAME) >/dev/null 2>&1 || true
	ROBODEV_LIVE_NAMESPACE=robodev \
	$(GO) test -tags=live -count=1 -v -timeout=1200s ./tests/e2e/ -run TestLive

e2e-test: ## Run end-to-end tests against the kind cluster
	@kubectl config use-context kind-$(KIND_CLUSTER_NAME) >/dev/null 2>&1 || true
	$(GO) test -tags=e2e -count=1 $(GOFLAGS) ./tests/e2e/...

integration-test: ## Run integration tests (Tier 2/3, no cluster needed)
	$(GO) test -tags=integration -count=1 $(GOFLAGS) ./tests/integration/...

test-report: ## Full orchestrated test run with markdown report
	./hack/run-integration-tests.sh

test-all: test integration-test ## Run unit + integration tests (no cluster needed)

logs: ## Stream controller logs
	kubectl logs -f -n $(HELM_NAMESPACE) -l app.kubernetes.io/name=robodev

# ---------------------------------------------------------------------------
# Live end-to-end testing (kind + real backends)
# ---------------------------------------------------------------------------

setup-secrets: ## Provision K8s secrets for live testing (interactive)
	@bash hack/setup-secrets.sh

live-deploy: ## Deploy to kind cluster with live values overlay
	helm upgrade --install $(HELM_RELEASE) charts/robodev/ \
		--namespace $(HELM_NAMESPACE) \
		-f charts/robodev/values.yaml \
		-f $(VALUES_LIVE) \
		--wait --timeout 120s

live-up: build docker-build-dev-controller docker-build-dev-engine-claude-code kind-create kind-load setup-secrets live-deploy ## Full live setup: build, cluster, secrets, deploy with real backends
	@echo ""
	@echo "RoboDev is running with live backends. Useful commands:"
	@echo "  make logs            — stream controller logs"
	@echo "  make live-redeploy   — rebuild and redeploy (reuses cluster + secrets)"
	@echo "  make local-down      — tear everything down"

live-redeploy: build docker-build-dev-controller docker-build-dev-engine-claude-code kind-load live-deploy ## Fast rebuild and redeploy with live values (reuses cluster)

# ---------------------------------------------------------------------------
# Docker Compose (local development without K8s)
# ---------------------------------------------------------------------------

compose-up: ## Start local development environment via Docker Compose
	docker compose up -d

compose-down: ## Stop and remove local development containers
	docker compose down

# ---------------------------------------------------------------------------
# Documentation site (MkDocs Material)
# ---------------------------------------------------------------------------

docs-serve: ## Serve documentation site locally on :8000
	pip install -q -r requirements.txt && mkdocs serve

docs-build: ## Build documentation site (strict mode)
	pip install -q -r requirements.txt && mkdocs build --strict

.DEFAULT_GOAL := help
