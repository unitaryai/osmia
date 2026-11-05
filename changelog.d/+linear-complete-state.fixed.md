**The Linear backend did not work against the real Linear API.** Every call
that Osmia makes failed:

- Polling sent the team ID as a `String!` where Linear expects an `ID`, so
  every poll was rejected with HTTP 400 and no ticket was ever picked up.
- `MarkInProgress` and `MarkFailed` called `issueAddLabel` with a `labelName`
  argument that does not exist. The labels are now looked up by name among the
  workspace's labels and the issue's team's labels, and created on the team if
  they are missing.
- `MarkComplete` sent the literal string `"completed"` as the state ID.
  Finished issues stayed open, but still got a "Task completed successfully"
  comment. The issue now moves to the state named by the new
  `completed_state_name` option, or else to the team's first completed-type
  state, normally "Done". The comment is posted only after the state change
  succeeds, and an update that Linear answers with `success: false` is reported
  as an error.

Osmia usually finishes with a pull request that nobody has reviewed yet, so a
review state such as "In Review" is often a better `completed_state_name`.
