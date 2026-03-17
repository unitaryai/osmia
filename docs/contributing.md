# Contributing to Osmia

Thank you for your interest in contributing to Osmia! This document provides guidelines and information for contributors.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](code-of-conduct.md). By participating, you are expected to uphold this code.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/osmia.git`
3. Install git hooks: `./hack/install-hooks.sh`
4. Create a feature branch: `git checkout -b feat/my-feature`
5. Make your changes
6. Commit with a conventional commit message
7. Push and open a pull request (hooks run lint and tests automatically)

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

## Git Hooks

Install the recommended git hooks after cloning:

```bash
./hack/install-hooks.sh
```

This installs a `pre-push` hook that runs `golangci-lint` and `go test -race ./...` before every push, catching lint errors and test failures locally before they reach CI.

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

## Releasing

Osmia uses git tags to trigger the release pipeline. The `release.yaml` workflow
builds container images and publishes the Helm chart to the GitHub Pages
repository (`https://unitaryai.github.io/osmia`).

### Release checklist

1. **Ensure `main` is clean** — all PRs merged, CI passing.
2. **Decide the version** — follow [Semantic Versioning](https://semver.org/):
   - Patch (`x.y.Z`) for bug fixes
   - Minor (`x.Y.0`) for new features (backward-compatible)
   - Major (`X.0.0`) for breaking changes
3. **Stamp `CHANGELOG.md`** — move the `[Unreleased]` section to a new
   `[x.y.z] - YYYY-MM-DD` heading. Add a fresh empty `[Unreleased]` above it.
4. **Bump `charts/osmia/Chart.yaml`** — set both `version` and `appVersion` to
   the new version (without the `v` prefix). The `chart-releaser-action` uses
   this to decide whether to publish; if it matches an already-published version
   the chart release is silently skipped.
5. **Commit** — `chore: release vX.Y.Z`
6. **Tag** — `git tag vX.Y.Z`
7. **Push both** — `git push && git push origin vX.Y.Z`
8. **Verify the release pipeline** — check GitHub Actions for:
   - Container images built and pushed to `ghcr.io`
   - Images signed with cosign
   - Helm chart published to the `gh-pages` branch
9. **Verify ArgoCD** (if applicable) — confirm the new chart version is
   available: `helm repo update && helm search repo osmia`

### Common mistakes

- **Forgetting `Chart.yaml`** — the most common failure. If `version` in
  `Chart.yaml` isn't bumped, the chart-releaser sees the version already exists
  in the `gh-pages` index and skips publishing. Container images are built but
  the Helm chart is not released.
- **Tag without pushing main** — the tag must point to a commit that is on
  `main` (or at least pushed to the remote), otherwise the release workflow
  checks out stale code.

## Plugin Contributions

If you're building a plugin, see the [Writing a Plugin](plugins/writing-a-plugin.md) guide.

## Licence

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
