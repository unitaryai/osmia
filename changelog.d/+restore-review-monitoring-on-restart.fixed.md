**A controller restart silently stopped Osmia following up on its PRs.** The
review poller held its list of watched PRs/MRs only in memory, so any
restart, including the cluster autoscaler moving the controller to a cheaper
node, made Osmia forget every PR it had opened; review comments posted
afterwards were never acted on. On dev-1 this happened to
gotyoucovered-wo!256 twenty minutes after it was opened, with five review
comments waiting. The list is now rebuilt from the TaskRun store at startup.
Comments that were already handled before the restart are not replayed, and
each PR's follow-up budget carries over. TaskRuns now also record the
ticket's title, description and repository, which fixes a related gap: a job
that finished after a restart had its PR silently left unwatched, because
registration depended on a ticket cache the restart had emptied. Requires a
persistent `taskrun_store`; PRs opened more than 30 days ago are not
restored.
