# Contributing to Osmia

Thank you for your interest in contributing to Osmia! This document provides guidelines and information for contributors.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/osmia.git`
3. Create a feature branch: `git checkout -b feat/my-feature`
4. Make your changes
5. Run tests: `make test`
6. Run linting: `make lint`
7. Commit with a conventional commit message
8. Push and open a pull request

## Development Prerequisites

- Go 1.23+
- Docker (for container builds)
- `kind` (for local Kubernetes clusters)
- `kubectl` (for cluster interaction)
- `helm` (for chart deployment)
- `gofumpt` (for code formatting)
- `golangci-lint` (for linting)
- `buf` (for protobuf linting and generation)

Install development dependencies:

```bash
./scripts/install-deps.sh
```

## Local Development Workflow

Osmia uses [kind](https://kind.sigs.k8s.io/) for local development and testing. The full workflow is automated via Make targets:

```bash
# Verify all prerequisites are installed
make check-prereqs

# Full setup: build the controller binary, build the controller image,
# create the kind cluster, and deploy the local-dev profile
make local-up

# Stream controller logs
make logs

# Run end-to-end smoke tests
make e2e-test

# Fast rebuild and redeploy (reuses existing cluster)
make local-redeploy

# Tear everything down
make local-down
```

The `local-up` target creates a two-node kind cluster (control-plane + worker),
builds the controller image with a `dev` tag, loads it into kind, and deploys
the Helm chart with local-dev overrides. The local-dev profile disables image
pulls and leader election, exposes the controller HTTP endpoint on
`localhost:30080` for `/healthz`, `/readyz`, and `/metrics`,
and uses the noop ticketing backend so the controller starts without external
credentials. Use `make live-up` when you want to exercise real ticketing
backends and engine containers.

## Code Style

- Run `gofumpt` on all Go files before committing
- Run `golangci-lint run` and fix all warnings
- Use British English in comments, documentation, and error messages
- Follow standard Go project layout conventions
- All exported types, functions, and methods must have doc comments
- Prefer table-driven tests with subtests

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` — new feature
- `fix:` — bug fix
- `docs:` — documentation only
- `test:` — adding or updating tests
- `refactor:` — code change that neither fixes a bug nor adds a feature
- `chore:` — maintenance tasks

## Pull Requests

- Keep PRs focused — one logical change per PR
- Update documentation as needed
- Add an entry to `CHANGELOG.md` under "Unreleased"
- Ensure CI passes before requesting review

## Plugin Contributions

If you're building a plugin, see [docs/plugins/writing-a-plugin.md](docs/plugins/writing-a-plugin.md).

## Licence

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
