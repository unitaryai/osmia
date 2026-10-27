# Quick start: Docker Compose

This guide gets Osmia running locally in about 5 minutes using Docker Compose. No Kubernetes cluster is needed, so it suits first-time users who want to see how Osmia works before deploying to a cluster.

!!! note "What Docker Compose mode does"
    Docker Compose runs the Osmia controller and a single AI agent engine as local containers. It polls your GitHub repository for labelled issues, runs an AI coding agent and opens pull requests. This is the same workflow as the Kubernetes deployment, without the cluster.

## Prerequisites

| Requirement | Minimum version | Notes |
|---|---|---|
| Docker | 24+ | With Docker Compose v2 (bundled with Docker Desktop) |
| GitHub repository | n/a | The repo the agent will work on |
| GitHub personal access token | n/a | With `repo` and `issues` scopes |
| Anthropic API key | n/a | Required for the Claude Code engine |

## 1. Clone the repository

```bash
git clone https://github.com/unitaryai/osmia.git
cd osmia
```

## 2. Create a `.env` File

Create a `.env` file in the repository root with your credentials:

```bash
cat > .env << 'EOF'
GITHUB_TOKEN=ghp_your_github_token_here
ANTHROPIC_API_KEY=sk-ant-your_anthropic_key_here
EOF
```

!!! warning "Keep your `.env` file safe"
    The `.env` file contains sensitive credentials. It is already listed in `.gitignore`. Never commit it to version control.

## 3. Configure Osmia

Edit the `docker-compose.yaml` environment section to point at your repository:

```yaml
environment:
  OSMIA_TICKETING_BACKEND: github
  OSMIA_TICKETING_OWNER: "your-org"        # ← your GitHub org or username
  OSMIA_TICKETING_REPO: "your-repo"        # ← your repository name
  OSMIA_TICKETING_LABELS: "osmia"
  OSMIA_ENGINE_DEFAULT: "claude-code"
```

## 4. Start Osmia

```bash
make compose-up
# or: docker compose up -d
```

Verify the controller is running:

```bash
docker compose logs -f osmia
```

You should see structured JSON log lines confirming that the ticketing poller has started.

## 5. Create a test issue

1. Open an issue in your target GitHub repository with a small, well-defined task:

    > **Title:** Add input validation to the /api/users endpoint
    >
    > **Body:** The POST handler for `/api/users` does not validate the `email` field. Add validation that rejects requests with a missing or malformed email address. Return a 400 status with a descriptive error message. Add unit tests for the new behaviour.

2. Add the `osmia` label to the issue.

3. Watch the controller logs. Within a few seconds you should see the task being picked up:

    ```bash
    docker compose logs -f osmia
    ```

## 6. Watch it work

The agent clones your repository, does the work, runs any tests it finds and opens a pull request. Progress updates appear as comments on the original issue.

```bash
# Check running containers
docker compose ps

# View agent logs
docker compose logs -f
```

## Stopping Osmia

```bash
make compose-down
# or: docker compose down
```

## Next steps

- [Deploy on Kubernetes](kubernetes.md): for production use with Helm
- [Configuration Reference](configuration.md): full config options
- [What is Osmia?](../concepts/what-is-osmia.md): the architecture
- [Guard Rails Overview](../concepts/guardrails-overview.md): the safety layers
