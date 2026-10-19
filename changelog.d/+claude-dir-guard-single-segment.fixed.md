**Every agent job failed at startup on the per-taskrun-pvc session backend.**
v0.5.0 added a safety check to `setup-claude.sh` that refuses to run when the
Claude configuration directory resolves somewhere dangerous, because the
skill and sub-agent directories under it are now cleared on every start. The
check demanded at least two path segments, but the per-taskrun-pvc store sets
`CLAUDE_CONFIG_DIR=/session`, which has one. Every job on that backend
exited immediately with "CLAUDE_DIR resolves to '/session', which is not a
nested absolute path". The check now refuses exactly what it exists to
prevent, `/` and `/skills` (where the ConfigMap volumes are mounted), and
accepts any other absolute path. The shared-pvc backend, which uses
`/session-store/claude`, was never affected.
