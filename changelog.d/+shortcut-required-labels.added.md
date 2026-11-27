**The Shortcut backend can take only stories carrying given labels.** A new
`labels` key in `ticketing.config` lists labels a story must all carry to be
picked up, named exactly as in Shortcut. Several Osmia instances can then
share a workspace when each has its own label; an instance without `labels`
still takes every story. `labels` cannot be used with `webhook.shortcut`, and
blank entries are refused. See
[Configuration](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#shortcut).
