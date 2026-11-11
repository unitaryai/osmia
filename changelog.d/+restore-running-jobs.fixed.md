**A controller restart no longer abandons agent jobs that are still
running.** The controller tracked running task runs in memory only, so after
a restart (an eviction, a rollout, the autoscaler moving the pod) it never
checked those jobs again: the agent finished, but its result was never
collected, the ticket was never completed, and the job no longer counted
towards `guardrails.max_concurrent_jobs`. On startup the controller now
re-adopts every `Running` task run whose Job still exists and re-reads the
agent's log, so the result, including the merge request URL, is collected as
normal. Task runs whose Job no longer exists are left unchanged for now. See
[Orphaned TaskRun Recovery](https://github.com/unitaryai/osmia/blob/main/docs/roadmap.md#orphaned-taskrun-recovery).
