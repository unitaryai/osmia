**Agent pods and the controller pod can now carry operator-set
annotations.** `execution.pod_annotations` adds annotations to every agent
pod on the `job` and `sandbox` backends, and the Helm chart's new
`podAnnotations` value does the same for the controller. The motivating use
is stopping a cluster autoscaler from evicting running work: node
consolidation had evicted the controller, which forgot every PR it was
watching, and killed a running review follow-up job part-way through.
Setting `karpenter.sh/do-not-disrupt: "true"` on both prevents that kind of
voluntary eviction; it does not protect against spot interruption or node
failure.
Annotations Osmia sets itself, such as the sandbox claim marker, take
precedence over configured ones, and malformed keys are rejected at startup
rather than surfacing as every agent Job failing to create.
