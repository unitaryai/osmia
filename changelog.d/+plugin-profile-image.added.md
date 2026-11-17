**A plugin profile can choose the engine image.** A profile under
`engines.claude-code.plugins.profiles` can now be written as a mapping with
`plugins` and `image`, so a ticket labelled `osmia-plugins:<profile>` runs on
that image instead of `engines.claude-code.image`. Build tasks can then use a
larger image with build tools, such as `engine-claude-code-build`, while every
other task keeps the default. Profiles written as a plain list of plugins work
as before. Two profiles on one ticket that set different images fail the task.
See [Plugins](https://github.com/unitaryai/osmia/blob/main/docs/plugins/engines.md#plugins).
