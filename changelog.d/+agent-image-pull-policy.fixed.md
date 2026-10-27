**Agent jobs could run an outdated engine image depending on which node they
landed on.** Osmia never set a pull policy on agent containers, so Kubernetes
applied its default, which for any tag other than `latest` is to download
the image only if the node does not already have it. Engine images are
usually referenced by a tag that moves, such as
`main`, which is rewritten on every build and by the weekly rebuild. A node
that had downloaded an earlier `main` kept running it indefinitely. In one
deployment this meant the per-taskrun-pvc fix shipped in v0.5.1 reached the registry
while jobs on one node kept failing with the bug it fixed, and jobs on other
nodes worked. Agent containers now re-check the registry on every start
unless the image is pinned by digest, in which case the cached copy is used.
This applies to standard jobs, sandboxed jobs and local Docker jobs. A job
on a tagged image now cannot start while the registry is unreachable.
Pinning by digest lets a node reuse its cached copy, but only a node that
already has that digest; to survive an outage the image must be pre-pulled
onto every node that can run agent jobs.
`docs/getting-started/configuration.md` explains both.
