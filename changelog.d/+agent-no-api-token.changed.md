**Agent pods no longer mount a Kubernetes service account token.** On the
`job` and `sandbox` backends every agent pod now sets
`automountServiceAccountToken: false`, whether or not
`execution.service_account_name` is set. Before, each agent pod got a token
for the Kubernetes API with whatever rights its service account held, which
agents never need. Cloud identity is unaffected: IRSA projects a token of its
own, and EKS Pod Identity and GKE Workload Identity serve credentials from an
agent on the node. Anything run inside an agent that relied on the in-cluster
token, such as `kubectl` in the build-tools engine image, must now be given
credentials explicitly: a kubeconfig, or cloud credentials from the pod's
cloud identity. See
[Giving agents a cloud identity](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#giving-agents-a-cloud-identity).
