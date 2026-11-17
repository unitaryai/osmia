**A plugin profile can start Claude in a tool's own folder.** A profile under
`engines.claude-code.plugins.profiles` can now set `workdir`, a directory in
the agent image that Claude starts in instead of `/workspace`, and `init`, an
executable in the image that runs just before Claude starts. Together they let
a tool whose skills expect to run from its own checkout ship that checkout in
its image and run as it does on a laptop. The prompt tells the agent where it
starts. A missing init script or working directory stops the job, and two
selected profiles that set different values fail the task. See
[Plugins](https://github.com/unitaryai/osmia/blob/main/docs/plugins/engines.md#plugins).
