**A build-tools variant of the Claude Code engine image.**
`ghcr.io/unitaryai/osmia/engine-claude-code-build` adds `uv` with Python
3.13, `copier`, the Temporal CLI, `kubectl`, Playwright with Chromium and
`@playwright/mcp`, `ffmpeg` and Xvfb to the engine image, for application
build work. Every tool is pinned and checksum-verified, Chromium runs under the
agent pod's restricted security settings, and the image is rebuilt with the
engine image. Select it with `engines.claude-code.image`. See
[Build-tools image](https://github.com/unitaryai/osmia/blob/main/docs/plugins/engines.md#build-tools-image).
