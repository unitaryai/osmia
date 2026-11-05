**No Linear ticket could start an agent job.** A TaskRun ID includes the ticket
ID, and the Job name is built from the TaskRun ID. Linear ticket IDs such as
`ENG-123` are uppercase, and Kubernetes rejects any name with an uppercase
letter, so every Linear Job was refused at creation. Job, task Secret and
session PVC names are now always valid Kubernetes names: they are lowercased,
and any character that is not allowed becomes a hyphen. A name that is too
long is shortened with a hash of the full name, so a run and its retry no
longer get the same Job name when their IDs differ only past the limit.
