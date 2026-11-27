**Agent pods can run under a named service account.** Set
`execution.service_account_name` and every agent pod on the `job` and
`sandbox` backends runs as that account, so a cloud role can be attached to
agents alone rather than to the namespace's `default` account, which every
other pod there shares. Unset, agent pods are unchanged. An invalid name is
rejected at startup. Osmia does not create the account; it must exist in the
namespace the agent jobs run in, and the controller refuses to start if it
does not, or if it cannot read it. The Helm chart's ClusterRole now grants
`get` on `serviceaccounts` for that check; a controller deployed without the
chart needs the same. Run from the chart, the controller also refuses its own
service account, which would give agents its rights to the cluster. With the
Helm chart, set
`config.execution.service_account_name`. See
[Configuration](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#giving-agents-a-cloud-identity).
