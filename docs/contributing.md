# Contributing to Osmia

This document explains how to contribute to Osmia.

## Code of conduct

This project adheres to the [Contributor Covenant Code of Conduct](code-of-conduct.md). By participating, you are expected to uphold this code.

## Getting started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/osmia.git`
3. Install git hooks: `./hack/install-hooks.sh`
4. Create a feature branch: `git checkout -b feat/my-feature`
5. Make your changes
6. Commit with a conventional commit message
7. Push and open a pull request (hooks run lint and tests automatically)

## Development prerequisites

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

## Git hooks

Install the recommended git hooks after cloning:

```bash
./hack/install-hooks.sh
```

This installs a `pre-push` hook that runs `golangci-lint` and `go test -race ./...` before every push, so lint errors and test failures show up locally before they reach CI.

## Local development workflow

Osmia uses [kind](https://kind.sigs.k8s.io/) for local development and testing. The full workflow is automated with Make targets:

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

### Incident.io MCP setup smoke test

`docker/engine-claude-code/setup-claude.sh` conditionally registers the
incident.io remote MCP server in the agent workspace's `.mcp.json` when
`INCIDENT_IO_API_KEY` is present. Because the script hardcodes the
container's absolute paths (`/etc/claude-code/*.json`, `/workspace/.mcp.json`),
it can only be exercised unmodified inside a container with that layout.
`hack/test-incident-mcp-setup.sh` runs it in a throwaway `alpine` container
via Docker and asserts the resulting `mcp.json` both with and without the
key set:

```bash
./hack/test-incident-mcp-setup.sh
```

This is a local developer convenience, not a CI gate. It is not wired
into any GitHub Actions workflow. It requires Docker (with the daemon
running) and `jq`, and skips cleanly (exit 0) when either is unavailable.

## Code style

- Run `gofumpt` on all Go files before committing
- Run `golangci-lint run` and fix all warnings
- Use British English in comments, documentation, and error messages
- Follow standard Go project layout conventions
- All exported types, functions, and methods must have doc comments
- Prefer table-driven tests with subtests

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:`: new feature
- `fix:`: bug fix
- `docs:`: documentation only
- `test:`: adding or updating tests
- `refactor:`: code change that neither fixes a bug nor adds a feature
- `chore:`: maintenance tasks

## Pull requests

- Keep PRs focused: one logical change per PR
- Update documentation as needed
- Add a changelog fragment (see below)
- Make sure CI passes before requesting review

### Changelog fragments

Do not edit `CHANGELOG.md`. Add a new file under `changelog.d/` instead,
named `+<slug>.<category>.md`, where the category is one of `added`,
`changed`, `deprecated`, `removed`, `fixed` or `security`:

```text
changelog.d/+wire-task-scoped-secrets.added.md
```

The contents are the entry as it should read in the released notes, written
as prose without a leading `-`. See
[`changelog.d/README.md`](https://github.com/unitaryai/osmia/blob/main/changelog.d/README.md)
for the full conventions.

One file per change means two PRs in flight never touch the same lines.
Appending to a shared "Unreleased" section conflicted every time, and every
merge then forced a rebase on everything behind it.

You do not need `towncrier` installed to add a fragment. It is run once per
release, by a maintainer.

Purely internal changes that no user or operator would notice need no
fragment. A refactor that changes observable behaviour does.

## Dev releases (edge deployments)

Every push to `main` automatically publishes:

- Edge container images tagged `main` (and `sha-<hash>`) to `ghcr.io`
- An edge Helm chart versioned `0.0.0-edge` to the same GitHub Pages repo

The `0.0.0-edge` chart is always overwritten with the latest `main` commit. Its
`appVersion` is set to the full git SHA so you can trace exactly which commit is
running.

### Weekly image rebuild

The image workflow also runs on a schedule, Mondays at 05:17 UTC, rebuilding
every image with `pull: true` so a fresh base layer is fetched. Nothing pins a
base-image digest, so this is the only way a security fix in `node:22-slim` or
the Go builder reaches the published images during a week with no commits.

**The schedule turns itself off if the repository goes quiet.** GitHub
disables scheduled workflows in a public repository after 60 days with no
repository activity. That is the situation the rebuild exists for: a long gap between commits is both when
a stale base image matters most and when the rebuild stops running.

There is no way to opt out of that behaviour. If the images look stale, check
whether the workflow is disabled:

```bash
gh workflow list --all
gh workflow enable images.yaml
```

It can also be re-enabled from the Actions tab. Pushing any commit to the
default branch resets the 60-day clock, but adding a keepalive commit purely
to hold the schedule open is not worth the noise in the history.

### Consuming the edge chart

```bash
helm repo add osmia https://unitaryai.github.io/osmia
helm repo update

# Inspect the dev chart (--devel required for pre-release versions)
helm show chart osmia/osmia --version 0.0.0-edge --devel

# Install/upgrade with the edge chart
helm upgrade --install osmia osmia/osmia \
  --version 0.0.0-edge --devel \
  --set image.tag=main \
  --set image.pullPolicy=Always
```

### Switching ArgoCD between dev and release mode

Dev mode tracks `main` continuously (no semver release needed):

```yaml
# kustomization.yaml
version: 0.0.0-edge

# values.yaml
image:
  tag: "main"
  pullPolicy: Always
engines:
  claude-code:
    image: ghcr.io/unitaryai/osmia/engine-claude-code:main
```

Release mode pins to a specific semver release:

```yaml
# kustomization.yaml
version: X.Y.Z

# values.yaml
image:
  tag: "X.Y.Z"
  pullPolicy: IfNotPresent
# remove engines.claude-code.image override (uses chart default)
```

### Dev vs full release

| Situation | Use |
|-----------|-----|
| Iterating on a fix: chart templates, RBAC, deployment config | Dev edge |
| Sharing a fix with external users or documenting in CHANGELOG | Full semver release |
| Testing a new feature before it's ready to announce | Dev edge |
| Stable, versioned deployment you can roll back to by number | Full semver release |

### Manual dev build

You can trigger the edge image + chart build without pushing to `main` via the
GitHub Actions UI: go to Actions → Build and push edge images → Run workflow.

## Releasing

Osmia uses git tags to trigger the release pipeline. The `release.yaml` workflow
builds container images and publishes the Helm chart to the GitHub Pages
repository (`https://unitaryai.github.io/osmia`).

### Release checklist

1. Make sure `main` is clean: all PRs merged, CI passing.
2. Decide the version, following [Semantic Versioning](https://semver.org/):
   - Patch (`x.y.Z`) for bug fixes
   - Minor (`x.Y.0`) for new features (backward-compatible)
   - Major (`X.0.0`) for breaking changes
3. Build `CHANGELOG.md`: assemble the fragments in `changelog.d/` into a
   new release section:

   ```bash
   uvx towncrier build --version x.y.z
   ```

   Pass the version without the `v` prefix, so the heading matches every
   prior release. Preview first with `--draft`, which writes nothing. The
   build consumes the fragments, so their deletion is part of the release
   commit. Configuration is in `towncrier.toml`.

   Entries are ordered alphabetically within each category, not by
   importance. Reorder by hand afterwards if a release has an entry that
   should lead.

   On a machine configured against Unitary's private package index, `uvx`
   will not find `towncrier` on that index. Prefix the command with
   `UV_NO_CONFIG=1` and add `--default-index https://pypi.org/simple`.
4. Bump `charts/osmia/Chart.yaml`: set both `version` and `appVersion` to
   the new version (without the `v` prefix). The `chart-releaser-action` uses
   this to decide whether to publish; if it matches an already-published version
   the chart release is silently skipped.
5. Commit: `chore: release vX.Y.Z`
6. Tag: `git tag vX.Y.Z`
7. Push both: `git push && git push origin vX.Y.Z`
8. Verify the release pipeline: check GitHub Actions for:
   - Container images built and pushed to `ghcr.io`
   - Images signed with cosign
   - Helm chart published to the `gh-pages` branch
9. Verify ArgoCD (if applicable): confirm the new chart version is
   available: `helm repo update && helm search repo osmia`

### Common mistakes

- Forgetting `Chart.yaml` is the most common failure. If `version` in
  `Chart.yaml` isn't bumped, the chart-releaser sees the version already exists
  in the `gh-pages` index and skips publishing. Container images are built but
  the Helm chart is not released.
- Tagging without pushing main: the tag must point to a commit that is on
  `main` (or at least pushed to the remote), otherwise the release workflow
  checks out stale code.

## Plugin contributions

If you are building a plugin, see the [Writing a Plugin](plugins/writing-a-plugin.md) guide.

## Licence

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
