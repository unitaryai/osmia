# Expansion to an OSS tool

There is a lot of hype on social media currently around agentic harness engineering. We have not yet seen anyone promoting a kubernetes-based enterprise-grade agentic harness so we think we can move Robodev to be such a tool. 

OpenClaw has taken the internet by storm becoming the fastest growing github repo in history, but it is often criticised for being full of security holes and issues. We need to pitch this as a super-secure enterprise agentic coding harness for organisations who already have infrastructure teams running kuberenetes. 

K8s should give us excellent scaling, observability, metrics, etc. We need to integrate with the best Kubernetes has to offer (certainly Karpenter for horizontal cluster scaling, prometheus, etc)

## What new capabilities and features do we need to think about?

- Claude Code now supports in-built agent teams mode (https://code.claude.com/docs/en/agent-teams)
- Integration with other popular ticketing systems: Jira, monday.com, clickup? This should be modular to allow contributors to add new ticketing functionality
- Secrets handling - integration with 1password / kubernetes external secrets / aws secretsmanager? again should be modular to allow expansion
- Guard-rails - enterprises are very nervous about AI use. Can we have an extra md file which is appended to all prompts with critical guard rails, or some sort of similar mechanism?
- Other channels for notification and user prompting when questions are asked. We have Slack but we should make this modular so people could plug in Teams, Telegram, etc. (we don't need to build all these initially, just provide the scaffolding)
- Claude Code has evolved a lot since we last worked on RoboDev, particularly around the Hooks, plugins, etc process. Research current docs to understand modern capabilities and interfaces (it's February 2026 now)
- We need to think about how we authenticate the claude code instances inside the k8s pods. Ideally we can somehow use the Anthropic Oauth so people on teams and enterprise plans can use their daily / session allowances rather than just pure API-key based pay-per-token. 


Possibly relevant reading:
 - https://x.com/charlierguo/status/2026009225663750512?s=20
 - https://x.com/Vtrivedy10/status/2023805578561060992
