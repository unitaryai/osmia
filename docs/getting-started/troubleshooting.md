# Troubleshooting

Common issues and their solutions when running Osmia.

## Controller Pod Is Not Starting

```bash
kubectl describe pod -n osmia -l app.kubernetes.io/name=osmia
kubectl logs -n osmia deployment/osmia --previous
```

| Symptom | Cause | Fix |
|---|---|---|
| **ImagePullBackOff** | Wrong image repository or tag, or missing pull secret | Check `image.repository` and `image.tag` in your values. Set `imagePullSecrets` if using a private registry. |
| **CrashLoopBackOff** | Configuration error | Inspect logs for the specific error. The most common issue is a missing or malformed `config` section. |
| **Pending** | Insufficient cluster resources | Check `kubectl describe pod` for scheduling events. Ensure your cluster has enough CPU and memory. |

## Issues Are Not Being Picked Up

- Confirm the issue has the correct label (must match `config.ticketing.config.labels` exactly).
- Check that `config.ticketing.config.owner` and `config.ticketing.config.repo` match the repository.
- Verify the GitHub token secret exists and has the required scopes:

```bash
kubectl get secret osmia-github-token -n osmia
```

- Look for polling errors in the controller logs:

```bash
kubectl logs -n osmia deployment/osmia | grep -i "ticketing"
```

!!! tip
    The controller polls on a configurable interval (default: 30 seconds). If you need faster pickup, consider enabling [webhooks](kubernetes.md#webhook-setup-optional) for near-instant ingestion.

## Agent Jobs Are Failing

```bash
# List recent jobs and their status
kubectl get jobs -n osmia

# Get logs from a failed job's pod
kubectl logs -n osmia job/<job-name>
```

| Symptom | Cause | Fix |
|---|---|---|
| **API key invalid** | Expired or incorrect API key | Verify your Anthropic or OpenAI secret contains a valid key. Recreate the secret if needed. |
| **Cost limit reached** | Job exceeded `max_cost_per_job` | Increase the limit in your guard rails config or simplify the task. |
| **Duration limit reached** | Job exceeded `max_job_duration_minutes` | Increase the limit or break the task into smaller pieces. |
| **Guard rail rejection** | Ticket failed validation | Check logs for the specific guard rail that rejected the ticket. Adjust `allowed_repos`, `allowed_task_types`, or `blocked_file_patterns` as needed. |

## Metrics Endpoint Is Not Working

Ensure `metrics.enabled` is set to `true` in your values (this is the default). The metrics are served on the port specified by `metrics.port` (default `8080`).

If you are using a `ServiceMonitor`, confirm that `metrics.serviceMonitor.enabled` is `true` and the labels match your Prometheus operator configuration.

```bash
# Test metrics endpoint directly
kubectl port-forward -n osmia deployment/osmia 8080:8080 &
curl -s http://localhost:8080/metrics | head -20
```

## Webhooks Are Not Working

- Verify the webhook pod port matches `webhook.port` in your values (default: 8081).
- Check that your ticketing provider is sending events to the correct URL.
- Verify the webhook secret matches between your provider and Osmia configuration.
- Check network policies allow inbound traffic on the webhook port.

```bash
# Check webhook server logs
kubectl logs -n osmia deployment/osmia | grep -i "webhook"
```

## Notifications Are Not Being Sent

- Verify the notification channel is correctly configured in your values.
- Check that the Slack bot token or Teams webhook URL is valid.
- Ensure the Slack bot has been invited to the target channel.
- Look for notification errors in the logs:

```bash
kubectl logs -n osmia deployment/osmia | grep -i "notification"
```

!!! info
    Notification failures are non-critical — they are logged but do not block the controller. Check the `osmia_plugin_errors_total` Prometheus metric for persistent failures.

## Docker Compose Issues

### Controller exits immediately

Check the logs for configuration errors:

```bash
docker compose logs osmia
```

Ensure your `.env` file contains valid `GITHUB_TOKEN` and `ANTHROPIC_API_KEY` values.

### Agent container cannot reach GitHub

Ensure Docker has network access and can resolve `api.github.com`. If you are behind a corporate proxy, configure Docker's proxy settings.

## Watchdog Is Terminating Jobs

The progress watchdog may terminate jobs that appear stalled, looping, or unproductive. Check the termination reason in the logs:

```bash
kubectl logs -n osmia deployment/osmia | grep -i "watchdog"
```

| Reason | What happened | Fix |
|---|---|---|
| **Loop detected** | Agent called the same tool repeatedly | The task may be too ambiguous. Provide clearer instructions in the issue. |
| **Thrashing** | High token use without file changes | Increase `thrashing_token_threshold` or add a longer `research_grace_period_minutes` for complex tasks. |
| **Stall** | No activity for extended period | Check if the agent container has network access to the AI API endpoint. |
| **Cost velocity** | Spending too fast | Reduce task complexity or increase `cost_velocity_max_per_10_min`. |

See [Guard Rails Overview](../concepts/guardrails-overview.md) for details on each detection rule and how to tune thresholds.

## Getting Help

If you cannot resolve an issue:

1. Search the [GitHub Issues](https://github.com/unitaryai/osmia/issues) for similar problems.
2. Open a new issue with the controller logs, your `values.yaml` (with secrets redacted), and a description of the expected vs actual behaviour.
3. Join the community discussion on GitHub Discussions.
