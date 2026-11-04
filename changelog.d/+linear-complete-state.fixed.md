**Linear issues were never moved to a completed state.** `MarkComplete` sent the
literal string `"completed"` as the `stateId`, but Linear requires the UUID of a
workflow state. Every update was rejected, so finished issues stayed open. The
backend now reads all of the issue team's workflow states of type `completed`
and moves the issue to the first one in board order (for example "Done"). It
also reports an error when Linear answers the update with `success: false`,
where it used to report the ticket as completed.
