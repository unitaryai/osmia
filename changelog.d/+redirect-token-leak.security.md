**API tokens no longer follow a redirect to another host.** The SCM backends
(GitHub, GitLab), the ticketing backends (Shortcut, GitHub, Linear), the Slack
plugins and the CodeRabbit review backend send a token on every request. Go's
HTTP client copies custom headers such as GitLab's `PRIVATE-TOKEN` and
Shortcut's `Shortcut-Token` onto a redirect to any host, and keeps
`Authorization` for the same host on another port, so an API that answered
with a redirect could hand the token to another server. These backends now
remove their credential headers whenever a redirect leaves the original scheme
and host, and refuse a redirect from https to http. The Vault secrets backend
is not yet covered; see
[roadmap item 35](https://github.com/unitaryai/osmia/blob/main/docs/roadmap.md).
