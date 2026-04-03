// Package claudecode implements the ExecutionEngine interface for the
// Claude Code CLI, translating tasks into execution specs that run
// Claude Code in headless mode inside Kubernetes Jobs.
package claudecode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// defaultImage is the container image used when no override is provided.
	defaultImage = "ghcr.io/unitaryai/engine-claude-code:latest"

	// defaultTimeoutSeconds is the default active deadline (2 hours).
	defaultTimeoutSeconds = 7200

	// defaultMaxTurns is the maximum number of agentic turns.
	defaultMaxTurns = 50

	// engineName is the unique identifier for this engine.
	engineName = "claude-code"

	// interfaceVersion is the version of the ExecutionEngine interface
	// this engine implements.
	interfaceVersion = 1

	// workspaceMountPath is where the workspace volume is mounted.
	workspaceMountPath = "/workspace"

	// configMountPath is where the configuration volume is mounted.
	configMountPath = "/config"

	// apiKeySecretName is the Kubernetes Secret containing the Anthropic API key.
	apiKeySecretName = "osmia-anthropic-key"

	// apiKeySecretKey is the key within apiKeySecretName that holds the API key.
	apiKeySecretKey = "api_key"

	// DefaultTaskResultSchema is the JSON schema for structured task results
	// returned by Claude Code when using --output-format stream-json with
	// --json-schema. This enforces a well-typed TaskResult contract between
	// the engine and the controller.
	DefaultTaskResultSchema = `{
  "type": "object",
  "properties": {
    "success": {"type": "boolean"},
    "summary": {"type": "string"},
    "merge_request_url": {"type": "string"},
    "branch_name": {"type": "string"},
    "tests_passed": {"type": "integer"},
    "tests_failed": {"type": "integer"},
    "tests_added": {"type": "integer"}
  },
  "required": ["success", "summary"]
}`
)

// Option is a functional option for configuring a ClaudeCodeEngine.
type Option func(*ClaudeCodeEngine)

// WithFallbackModel sets the fallback model used when the primary model is
// overloaded or unavailable (e.g. "haiku").
func WithFallbackModel(model string) Option {
	return func(e *ClaudeCodeEngine) {
		e.fallbackModel = model
	}
}

// WithToolWhitelist sets the list of tools the agent is allowed to use.
// When set, only these tools will be available via --allowedTools.
func WithToolWhitelist(tools []string) Option {
	return func(e *ClaudeCodeEngine) {
		e.toolWhitelist = tools
	}
}

// WithJSONSchema sets the JSON schema for structured output. When set,
// the engine uses --output-format stream-json with --json-schema instead
// of the default --output-format json.
func WithJSONSchema(schema string) Option {
	return func(e *ClaudeCodeEngine) {
		e.jsonSchema = schema
	}
}

// WithTeamsConfig sets the agent teams configuration. When enabled, the
// engine sets team-related environment variables and appends --teammate-mode
// to the execution spec. Agent teams spawn multiple independent Claude Code
// instances; this is distinct from sub-agents (see WithSubAgents).
func WithTeamsConfig(cfg TeamsConfig) Option {
	return func(e *ClaudeCodeEngine) {
		e.teamsConfig = cfg
	}
}

// WithSubAgents sets the sub-agent definitions. Inline sub-agents are passed
// via the --agents CLI flag; ConfigMap-backed sub-agents are written to
// ~/.claude/agents/ via setup-claude.sh.
func WithSubAgents(agents []SubAgent) Option {
	return func(e *ClaudeCodeEngine) {
		e.subAgents = agents
	}
}

// WithMaxTurns overrides the default maximum number of agentic turns passed to
// Claude Code via --max-turns. Use this for tasks that require more steps than
// the default (50).
func WithMaxTurns(n int) Option {
	return func(e *ClaudeCodeEngine) {
		e.maxTurns = n
	}
}

// WithSkills sets the custom skills to make available to the agent.
// Each skill is written to ~/.claude/skills/<name>.md before the agent
// starts, allowing the agent to invoke it via /skill-name in its prompts.
func WithSkills(skills []Skill) Option {
	return func(e *ClaudeCodeEngine) {
		e.skills = skills
	}
}

// WithSessionStore configures session persistence for the engine. When set,
// BuildExecutionSpec adds the session store's volume mounts and environment
// variables to the spec, and includes the appropriate --session-id or --resume
// flag depending on whether this is a first run or a retry.
func WithSessionStore(store engine.SessionStore) Option {
	return func(e *ClaudeCodeEngine) {
		e.sessionStore = store
	}
}

// osmiaSessionNamespace is the UUID v5 namespace used to derive deterministic
// session IDs from task IDs. Using a fixed namespace ensures that the same
// task always produces the same session ID, making the controller and engine
// independently consistent without coordination.
var osmiaSessionNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// ClaudeCodeEngine implements engine.ExecutionEngine for the Claude Code CLI.
type ClaudeCodeEngine struct {
	fallbackModel string
	maxTurns      int
	toolWhitelist []string
	jsonSchema    string
	teamsConfig   TeamsConfig
	skills        []Skill
	subAgents     []SubAgent
	sessionStore  engine.SessionStore
}

// New returns a new ClaudeCodeEngine with the given functional options applied.
func New(opts ...Option) *ClaudeCodeEngine {
	e := &ClaudeCodeEngine{}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name returns the unique engine identifier.
func (e *ClaudeCodeEngine) Name() string {
	return engineName
}

// InterfaceVersion returns the version of the ExecutionEngine interface
// this engine implements.
func (e *ClaudeCodeEngine) InterfaceVersion() int {
	return interfaceVersion
}

// effectiveMaxTurns returns the configured max turns, falling back to the
// package default when none was set.
func (e *ClaudeCodeEngine) effectiveMaxTurns() int {
	if e.maxTurns > 0 {
		return e.maxTurns
	}
	return defaultMaxTurns
}

// BuildExecutionSpec translates a task and engine configuration into an
// engine-agnostic ExecutionSpec for running Claude Code in headless mode.
func (e *ClaudeCodeEngine) BuildExecutionSpec(task engine.Task, config engine.EngineConfig) (*engine.ExecutionSpec, error) {
	if task.ID == "" {
		return nil, fmt.Errorf("task ID must not be empty")
	}

	prompt, err := e.BuildPrompt(task)
	if err != nil {
		return nil, fmt.Errorf("building prompt: %w", err)
	}

	image := config.Image
	if image == "" {
		image = defaultImage
	}

	timeout := config.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeoutSeconds
	}

	// Always use stream-json so that events are written to stdout
	// incrementally — this makes kubectl logs useful during execution and
	// feeds the agentstream reader without any extra configuration.
	jsonSchema := config.JSONSchema
	if jsonSchema == "" {
		jsonSchema = e.jsonSchema
	}
	if jsonSchema == "" {
		jsonSchema = DefaultTaskResultSchema
	}

	// setup-claude.sh writes ~/.claude/settings.json (MCP tool permissions)
	// and /workspace/.mcp.json (server registration) before exec'ing claude.
	// The home directory is an emptyDir volume so these files must be created
	// at container startup; they cannot be baked into the image.
	command := []string{
		"setup-claude.sh",
		"-p", prompt,
		"--output-format", "stream-json",
		"--max-turns", strconv.Itoa(e.effectiveMaxTurns()),
		"--dangerously-skip-permissions",
		"--verbose",                            // richer event data (tool calls, cost breakdowns)
		"--mcp-config", "/workspace/.mcp.json", // explicit load path written by setup-claude.sh
	}

	if jsonSchema != "" {
		command = append(command, "--json-schema", jsonSchema)
	}

	// Resolve fallback model: config takes precedence over engine-level default.
	fallbackModel := config.FallbackModel
	if fallbackModel == "" {
		fallbackModel = e.fallbackModel
	}
	if fallbackModel != "" {
		command = append(command, "--fallback-model", fallbackModel)
	}

	// Handle session persistence: --resume resumes an existing session,
	// --session-id pins the session ID for a new one so retry pods know
	// which session to resume on the next attempt.
	if e.sessionStore != nil {
		if task.SessionID != "" {
			command = append(command, "--resume", task.SessionID)
		} else {
			sessionID := sessionIDForTask(task.ID)
			command = append(command, "--session-id", sessionID)
		}
	}

	if config.AppendSystemPrompt != "" {
		command = append(command, "--append-system-prompt", config.AppendSystemPrompt)
	}

	// Resolve tool whitelist: config takes precedence over engine-level default.
	toolWhitelist := config.ToolWhitelist
	if len(toolWhitelist) == 0 {
		toolWhitelist = e.toolWhitelist
	}
	if len(toolWhitelist) > 0 {
		command = append(command, "--allowedTools", strings.Join(toolWhitelist, ","))
	}

	if len(config.ToolBlacklist) > 0 {
		command = append(command, "--disallowedTools", strings.Join(config.ToolBlacklist, ","))
	}

	// Append agent teams flags when teams are enabled.
	command = append(command, TeamsFlags(e.teamsConfig)...)

	env := map[string]string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"OSMIA_TASK_ID":   task.ID,
		"OSMIA_TICKET_ID": task.TicketID,
		"OSMIA_REPO_URL":  task.RepoURL,
	}

	// Merge teams environment variables when teams are enabled.
	for k, v := range TeamsEnvVars(e.teamsConfig) {
		env[k] = v
	}

	// Inject skill files as environment variables.
	// setup-claude.sh decodes these and writes ~/.claude/skills/<name>.md
	// before starting the agent.
	for k, v := range SkillEnvVars(e.skills) {
		env[k] = v
	}

	// Append sub-agent flags for inline sub-agents.
	agentFlags, err := SubAgentFlag(e.subAgents)
	if err != nil {
		return nil, fmt.Errorf("building sub-agent flags: %w", err)
	}
	command = append(command, agentFlags...)

	// Merge sub-agent env vars (for ConfigMap-backed sub-agents).
	for k, v := range SubAgentEnvVars(e.subAgents) {
		env[k] = v
	}

	// Merge any extra environment variables from the engine config.
	for k, v := range config.Env {
		env[k] = v
	}

	apiSecretName := apiKeySecretName
	if config.APIKeySecret != "" {
		apiSecretName = config.APIKeySecret
	}
	secretKeyRefs := map[string]engine.SecretKeyRef{
		"ANTHROPIC_API_KEY": {SecretName: apiSecretName, Key: apiKeySecretKey},
	}
	// Merge any additional secret refs from the engine config (e.g. SCM tokens).
	for k, v := range config.SecretKeyRefs {
		secretKeyRefs[k] = v
	}

	volumes := []engine.VolumeMount{
		{
			Name:      "workspace",
			MountPath: workspaceMountPath,
		},
		{
			Name:      "config",
			MountPath: configMountPath,
			ReadOnly:  true,
		},
		{
			// Shadow the read-only container home directory with a writable
			// emptyDir so Claude Code can create ~/.claude/ for its config.
			Name:      "home",
			MountPath: "/home/osmia",
		},
		{
			// Provide a writable /tmp so Claude Code can create its subprocess
			// shell directories (e.g. /tmp/claude-<uid>/) for Bash tool execution.
			Name:      "tmp",
			MountPath: "/tmp",
		},
	}

	// Append ConfigMap volumes for skills and sub-agents.
	volumes = append(volumes, SkillVolumes(e.skills)...)
	volumes = append(volumes, SubAgentVolumes(e.subAgents)...)

	// Merge session store volumes and environment variables when persistence
	// is configured. TaskRunID isolates storage per execution attempt so
	// retries of the same ticket do not share session data.
	if e.sessionStore != nil {
		taskRunID := task.TaskRunID
		if taskRunID == "" {
			// Fallback for callers that don't set TaskRunID (e.g. tests).
			taskRunID = task.ID
		}
		sessionID := task.SessionID
		if sessionID == "" {
			sessionID = sessionIDForTask(taskRunID)
		}
		volumes = append(volumes, e.sessionStore.VolumeMounts(taskRunID)...)
		for k, v := range e.sessionStore.Env(taskRunID, sessionID) {
			env[k] = v
		}
	}

	spec := &engine.ExecutionSpec{
		Image:                 image,
		Command:               command,
		Env:                   env,
		SecretKeyRefs:         secretKeyRefs,
		ResourceRequests:      config.ResourceRequests,
		ResourceLimits:        config.ResourceLimits,
		Volumes:               volumes,
		ActiveDeadlineSeconds: timeout,
	}

	return spec, nil
}

// sessionIDForTask derives a deterministic UUID v5 session ID from a task ID.
// Using a deterministic scheme means the controller and engine independently
// arrive at the same session ID without coordination, simplifying retry logic.
func sessionIDForTask(taskID string) string {
	return uuid.NewSHA1(osmiaSessionNamespace, []byte("osmia/session/"+taskID)).String()
}

// BuildPrompt constructs the task prompt for Claude Code from the task's
// title, description, repository URL, and any additional metadata.
func (e *ClaudeCodeEngine) BuildPrompt(task engine.Task) (string, error) {
	if task.Title == "" {
		return "", fmt.Errorf("task title must not be empty")
	}

	var b strings.Builder

	b.WriteString("# Task: ")
	b.WriteString(task.Title)
	b.WriteString("\n\n")

	if task.Description != "" {
		b.WriteString("## Description\n\n")
		b.WriteString(task.Description)
		b.WriteString("\n\n")
	}

	if task.TicketURL != "" {
		b.WriteString("## Ticket\n\n")
		b.WriteString(task.TicketURL)
		b.WriteString("\n\n")
	}

	if task.RepoURL != "" {
		b.WriteString("## Repository\n\n")
		b.WriteString(task.RepoURL)
		b.WriteString("\n\n")
	}

	// When a session is being resumed via --resume, the conversation history
	// is already available to the agent — skip the Continuation section to
	// avoid confusing it with redundant git-clone instructions.
	if task.PriorBranchName != "" && task.SessionID == "" {
		b.WriteString("## Continuation\n\n")
		b.WriteString("A previous agent session worked on this task but was interrupted before\n")
		b.WriteString("completing it (max turns reached or premature exit). Prior work was pushed\n")
		b.WriteString("to branch `")
		b.WriteString(task.PriorBranchName)
		b.WriteString("`.\n\n")
		b.WriteString("Start by cloning that branch to recover the previous progress:\n")
		b.WriteString("   ```\n")
		b.WriteString("   git clone --branch ")
		b.WriteString(task.PriorBranchName)
		b.WriteString(" --depth=50 ")
		b.WriteString(task.RepoURL)
		b.WriteString(" /workspace/repo\n")
		b.WriteString("   ```\n\n")
		b.WriteString("Then run:\n")
		b.WriteString("   ```\n")
		b.WriteString("   git log --oneline -20\n")
		b.WriteString("   ```\n\n")
		b.WriteString("to see what was already completed, and continue from where the prior\n")
		b.WriteString("agent left off. Do not redo work that is already committed.\n\n")
	}

	if len(task.Labels) > 0 {
		b.WriteString("## Labels\n\n")
		b.WriteString(strings.Join(task.Labels, ", "))
		b.WriteString("\n\n")
	}

	if len(task.Metadata) > 0 {
		b.WriteString("## Additional Context\n\n")
		for k, v := range task.Metadata {
			b.WriteString("- **")
			b.WriteString(k)
			b.WriteString("**: ")
			b.WriteString(v)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Instructions\n\n")

	if task.RepoURL != "" {
		branchName := "osmia/" + task.TicketID

		b.WriteString("**IMPORTANT: You MUST open a merge request (MR/PR) before completing the task\n")
		b.WriteString("if you have created, modified, or deleted ANY files — including documentation,\n")
		b.WriteString("plans, configuration, and generated output. Never skip this step.**\n\n")

		// When resuming a persisted session, the workspace and git state are
		// already on the PVC — skip clone/checkout instructions entirely.
		if task.SessionID != "" {
			b.WriteString("1. The workspace is already present from your previous session. Continue working in the existing directory.\n\n")
			b.WriteString("2. Commit and push your changes to branch `")
			b.WriteString(branchName)
			b.WriteString("` at logical checkpoints:\n")
			b.WriteString("   ```\n")
			b.WriteString("   git add -A && git commit -m \"wip: <short description>\"\n")
			b.WriteString("   git push origin ")
			b.WriteString(branchName)
			b.WriteString("\n")
			b.WriteString("   ```\n\n")
			b.WriteString("3. Open a merge request. This step is MANDATORY — do not skip it.\n")
			b.WriteString("   Use `glab` (for GitLab) or `gh` (for GitHub).\n")
			b.WriteString("   Write a clear, well-structured MR description covering: what changed, why, and how to verify.\n")
			writeMRTitleGuidance(&b, task)
			b.WriteString("   Example for GitLab:\n")
			b.WriteString("   ```\n")
			b.WriteString("   cd /workspace/repo\n")
			b.WriteString("   glab auth login --hostname gitlab.com --token \"$GITLAB_TOKEN\"\n")
			b.WriteString("   glab mr create --fill --title \"<concise title>")
			writeMRTitleSuffix(&b, task)
			b.WriteString("\" --description \"<full description>")
			writeMRDescriptionFooter(&b, task)
			b.WriteString("\"\n")
			b.WriteString("   ```\n\n")
			b.WriteString("4. When the full task is complete, write /workspace/result.json containing:\n")
			b.WriteString("   `{\"success\": true, \"summary\": \"<one-line summary>\", \"branch_name\": \"")
			b.WriteString(branchName)
			b.WriteString("\", \"merge_request_url\": \"<MR URL from step 3>\"}`\n")
		} else {
			b.WriteString("1. Configure git globally:\n")
			b.WriteString("   ```\n")
			b.WriteString("   git config --global user.name \"Osmia\"\n")
			b.WriteString("   git config --global user.email \"osmia@localhost\"\n")
			b.WriteString("   git config --global init.defaultBranch main\n")
			b.WriteString("   # Configure git credentials from SCM token env vars\n")
			b.WriteString("   if [ -n \"${GITLAB_TOKEN:-}\" ]; then\n")
			b.WriteString("     git config --global credential.helper store\n")
			b.WriteString("     echo \"https://oauth2:${GITLAB_TOKEN}@gitlab.com\" >> ~/.git-credentials\n")
			b.WriteString("   fi\n")
			b.WriteString("   if [ -n \"${GITHUB_TOKEN:-}\" ]; then\n")
			b.WriteString("     git config --global credential.helper store\n")
			b.WriteString("     echo \"https://x-access-token:${GITHUB_TOKEN}@github.com\" >> ~/.git-credentials\n")
			b.WriteString("   fi\n")
			b.WriteString("   ```\n\n")

			if task.PriorBranchName == "" {
				b.WriteString("2. Clone the repository to /workspace/repo:\n")
				b.WriteString("   ```\n")
				b.WriteString("   git clone --depth=1 ")
				b.WriteString(task.RepoURL)
				b.WriteString(" /workspace/repo\n")
				b.WriteString("   ```\n\n")
			}

			b.WriteString("3. Create a branch for your changes (use the same branch on every retry so work is not lost):\n")
			b.WriteString("   ```\n")
			b.WriteString("   git checkout -b ")
			b.WriteString(branchName)
			b.WriteString("\n")
			b.WriteString("   ```\n\n")
			b.WriteString("4. Commit and push your changes to that branch at logical checkpoints:\n")
			b.WriteString("   ```\n")
			b.WriteString("   git add -A && git commit -m \"wip: <short description>\"\n")
			b.WriteString("   git push origin ")
			b.WriteString(branchName)
			b.WriteString("\n")
			b.WriteString("   ```\n\n")
			b.WriteString("5. Open a merge request. This step is MANDATORY — do not skip it.\n")
			b.WriteString("   Use `glab` (for GitLab) or `gh` (for GitHub).\n")
			b.WriteString("   Write a clear, well-structured MR description covering: what changed, why, and how to verify.\n")
			writeMRTitleGuidance(&b, task)
			b.WriteString("   Example for GitLab:\n")
			b.WriteString("   ```\n")
			b.WriteString("   cd /workspace/repo\n")
			b.WriteString("   glab auth login --hostname gitlab.com --token \"$GITLAB_TOKEN\"\n")
			b.WriteString("   glab mr create --fill --title \"<concise title>")
			writeMRTitleSuffix(&b, task)
			b.WriteString("\" --description \"<full description>")
			writeMRDescriptionFooter(&b, task)
			b.WriteString("\"\n")
			b.WriteString("   ```\n\n")
			b.WriteString("6. When the full task is complete, write /workspace/result.json containing:\n")
			b.WriteString("   `{\"success\": true, \"summary\": \"<one-line summary>\", \"branch_name\": \"")
			b.WriteString(branchName)
			b.WriteString("\", \"merge_request_url\": \"<MR URL from step 5>\"}`\n")
		}
	} else {
		b.WriteString("Complete the task described above. Work in the /workspace directory.\n")
		b.WriteString("Write a result.json file to /workspace/result.json when finished.\n")
	}

	return b.String(), nil
}

// writeMRTitleGuidance writes instructions telling the agent to include the
// ticket ID in the MR title, if a ticket URL is available.
func writeMRTitleGuidance(b *strings.Builder, task engine.Task) {
	if task.TicketID == "" {
		return
	}
	b.WriteString("   The MR title MUST end with ` [")
	b.WriteString(task.TicketID)
	b.WriteString("]` so the ticket is traceable from the MR.\n")
}

// writeMRTitleSuffix appends the ticket ID suffix for the glab/gh example
// command, e.g. ` [sc-12345]`.
func writeMRTitleSuffix(b *strings.Builder, task engine.Task) {
	if task.TicketID == "" {
		return
	}
	b.WriteString(" [")
	b.WriteString(task.TicketID)
	b.WriteString("]")
}

// writeMRDescriptionFooter appends a ticket reference line for the glab/gh
// example command.
func writeMRDescriptionFooter(b *strings.Builder, task engine.Task) {
	if task.TicketURL == "" {
		return
	}
	b.WriteString("\n\nReferences: ")
	b.WriteString(task.TicketURL)
}
