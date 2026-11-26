**The Shortcut backend can take only stories carrying given labels.** A new
`labels` key in `ticketing.config` lists labels a story must all carry to be
picked up, compared exactly, as `exclude_labels` is. The labels are added to
the search query and checked again on every story returned. This lets several
Osmia instances poll the same workspace, state and owner without each taking
the same story: give each instance its own label. A story carrying two
instances' labels is taken by both. Because the Shortcut webhook takes stories
whatever their labels, `labels` on the Shortcut backend together with
`webhook.shortcut` is refused at start-up. Without `labels`, nothing changes.
See
[Configuration](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#shortcut).
