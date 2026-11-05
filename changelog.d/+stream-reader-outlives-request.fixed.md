**Runs started by a webhook lost their agent stream at once.** The webhook
handlers pass the HTTP request context to the controller, and the agent's
stream reader was derived from it. The request context is cancelled as soon as
the webhook response is written, so the reader stopped before it read anything.
The run's result, its merge request URL, the watchdog telemetry and review
tracking were all lost for GitHub, GitLab, Shortcut, Slack and generic webhook
tickets. The same applied to memory extraction after a Slack approval. Stream
readers and memory extraction now run until the controller itself stops.
