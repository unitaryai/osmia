**Linear issues were never moved to a completed state.** `MarkComplete` sent the
literal string `"completed"` as the `stateId`, but Linear requires the UUID of a
workflow state. Every update was rejected, so finished issues stayed open. The
backend now moves the issue to the state named by the new
`completed_state_name` option, as the Shortcut backend already allows. Without
that option, it uses the team's first state of type `completed` in board order,
normally "Done". Osmia usually finishes with a pull request that nobody has
reviewed yet, so a review state such as "In Review" is often a better target.
The completion comment is now posted only after the state change succeeds, and
an update that Linear answers with `success: false` is reported as an error.
