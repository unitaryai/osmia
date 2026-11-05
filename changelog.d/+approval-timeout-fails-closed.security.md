**An approval gate that nobody answered launched the agent anyway.** When the
watchdog's `unanswered_human_timeout_minutes` passed for a run held at an
approval gate, the controller treated it as a failed job and retried it. For a
`pre_start` gate, that retry started the agent job that nobody had approved.
For `pre_merge` and continuation gates, it ran the agent again. An unanswered
gate now fails the run for good: no retry or fallback, the ticket is marked
failed, and the pending approval request is cancelled.
