# Getting started

Pick the path that fits your situation.

## Step 1: Deploy Osmia

<div class="grid cards" markdown>

-   :material-docker:{ .lg .middle } **Try it locally first**

    ---

    No Kubernetes cluster needed. Get Osmia running with Docker Compose in 5 minutes.

    Best for first-time users, quick evaluation and learning how Osmia works.

    [:octicons-arrow-right-24: Docker Compose quick start](docker-compose.md)

-   :material-kubernetes:{ .lg .middle } **Deploy to Kubernetes**

    ---

    Deploy Osmia with Helm on any Kubernetes cluster.

    Best for Kubernetes engineers, production deployments and team evaluations.

    [:octicons-arrow-right-24: Kubernetes quick start](kubernetes.md)

</div>

## Step 2: Connect your tools

Once Osmia is running, follow the guide for your ticketing and notification setup:

<div class="grid cards" markdown>

-   :material-github:{ .lg .middle } **GitHub Issues + Slack**

    ---

    Picks up GitHub issues labelled `osmia`, posts results to Slack and opens pull requests.

    [:octicons-arrow-right-24: GitHub Issues + Slack guide](github-issues-slack.md)

-   :material-lightning-bolt:{ .lg .middle } **Shortcut + Slack**

    ---

    Picks up Shortcut stories assigned to `@osmia`, moves them through workflow states and posts results to Slack.

    [:octicons-arrow-right-24: Shortcut + Slack guide](shortcut-slack.md)

-   :material-ticket:{ .lg .middle } **Linear + Slack**

    ---

    Picks up Linear issues labelled `osmia`, tracks them with labels and posts results to Slack.

    [:octicons-arrow-right-24: Linear + Slack guide](linear-slack.md)

</div>

## After setup

- [Configuration Reference](configuration.md): every config option
- [Troubleshooting](troubleshooting.md): common issues and how to fix them
- [What is Osmia?](../concepts/what-is-osmia.md): the concepts behind the tool
- [Guard Rails Overview](../concepts/guardrails-overview.md): the safety layers
