**Osmia can fix a failed CI pipeline on a merge request it opened.** With
`review_response.fix_failed_pipelines: true`, when the latest pipeline on a
watched MR fails, Osmia starts a follow-up job on the same branch and gives it
the failed jobs and the end of each job's log. Each failed run is acted on
once, and these follow-ups count against `max_follow_up_jobs`, so a pipeline
that fails for a reason the agent cannot fix stops being retried. Works with
the GitHub and GitLab backends; off by default. See
[Failed pipelines](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#failed-pipelines).
