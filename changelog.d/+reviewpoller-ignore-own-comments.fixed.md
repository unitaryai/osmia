**Osmia could reply to its own comments in a loop.** The review poller
ignored Osmia's own comments only when its SCM account's username contained
`osmia`. A deployment whose bot account was named anything else — such as one
created before the project was renamed — had its own comments treated as
review feedback. A follow-up job ends by posting a summary like "Addressed.
Fixed the 2 pyright errors", and those words are exactly what the classifier
treats as actionable, so each follow-up spawned the next until
`max_follow_up_jobs` ran out, wasting agent runs and the MR's follow-up budget.
The GitHub and GitLab backends now report which account their token belongs
to (`GET /user`), and the poller ignores every comment from that account,
inline or not, whatever it is called. If the lookup fails, as it does for a
GitHub App installation token, the poller logs a warning and falls back to the
name-based check.
