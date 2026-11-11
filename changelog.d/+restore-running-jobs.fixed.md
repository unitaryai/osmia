**A controller restart no longer abandons agent jobs that are still
running, when TaskRuns are kept in a persistent store.** The controller
tracked running task runs in memory only, so after a restart (an eviction, a
rollout, the autoscaler moving the pod) it never checked those jobs again:
the agent finished, but its result was never collected, the ticket was never
completed, and the job no longer counted towards
`guardrails.max_concurrent_jobs`. With a persistent `taskrun_store` (for
example `backend: sqlite`), the controller now re-adopts on startup every
`Running` task run whose Job still exists and re-reads the agent's log, so
the result, including the merge request URL, is collected as normal, and the
run can still fall back to the next engine if it fails. With the default
in-memory store there is nothing to re-adopt. Task runs whose Job no longer
exists are left unchanged for now. Two related fixes: the agent's log is now
read from the pod of the run's current Job, which continuation, retry and
fallback Jobs previously missed, and a fallback launch is now saved to the
store. See
[Orphaned TaskRun Recovery](https://github.com/unitaryai/osmia/blob/main/docs/roadmap.md#orphaned-taskrun-recovery).
