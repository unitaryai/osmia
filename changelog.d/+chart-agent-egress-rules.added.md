**The agent's egress rules can be replaced.** With `networkPolicy.enabled`,
the agent NetworkPolicy always allowed DNS, TCP 443 and TCP 22 to anywhere,
and `networkPolicy.agent.extraEgressRules` could only add to that. Setting
`networkPolicy.agent.egressRules` to a list now renders that list in place of
the three default rules, DNS included, so agents can be limited to, say, a
model endpoint, a git host and a package index. `extraEgressRules` is still
added after it, and an empty list allows no egress beyond it. Leaving
`egressRules` unset keeps the policy exactly as before. See
[Network isolation](https://github.com/unitaryai/osmia/blob/main/docs/security.md#network-isolation).
