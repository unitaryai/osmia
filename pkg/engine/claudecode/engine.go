// Package claudecode implements the ExecutionEngine interface for the
// Claude Code CLI, translating tasks into execution specs that run
// Claude Code in headless mode inside Kubernetes Jobs.
package claudecode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/unitaryai/robodev/pkg/engine"
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

	// mcpConfigPath is the path to the MCP server configuration baked into the image.
	mcpConfigPath = "/etc/claude-code/mcp.json"

	// apiKeySecretName is the Kubernetes Secret containing the Anthropic API key.
	apiKeySecretName = "robodev-anthropic-key"

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
// engine appends --agents flags and team-related environment variables
// to the execution spec.
func WithTeamsConfig(cfg TeamsConfig) Option {
	return func(e *ClaudeCodeEngine) {
		e.teamsConfig = cfg
	}
}

// ClaudeCodeEngine implements engine.ExecutionEngine for the Claude Code CLI.
type ClaudeCodeEngine struct {
	fallbackModel string
	toolWhitelist []string
	jsonSchema    string
	teamsConfig   TeamsConfig
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

	// setup-claude.sh writes ~/.claude/settings.json (MCP tool permissions)
	// and /workspace/.mcp.json (server registration) before exec'ing claude.
	// The home directory is an emptyDir volume so these files must be created
	// at container startup; they cannot be baked into the image.
	command := []string{
		"setup-claude.sh",
		"-p", prompt,
		"--output-format", "stream-json",
		"--max-turns", strconv.Itoa(defaultMaxTurns),
		"--dangerously-skip-permissions",
		"--verbose",                           // richer event data (tool calls, cost breakdowns)
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

	if config.NoSessionPersistence {
		command = append(command, "--no-session-persistence")
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
	taskType := task.Metadata["task_type"]
	teamFlags, err := BuildAgentFlags(e.teamsConfig, taskType)
	if err != nil {
		return nil, fmt.Errorf("building agent team flags: %w", err)
	}
	command = append(command, teamFlags...)

	env := map[string]string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"ROBODEV_TASK_ID":                          task.ID,
		"ROBODEV_TICKET_ID":                        task.TicketID,
		"ROBODEV_REPO_URL":                         task.RepoURL,
	}

	// Merge teams environment variables when teams are enabled.
	for k, v := range TeamsEnvVars(e.teamsConfig) {
		env[k] = v
	}

	// Merge any extra environment variables from the engine config.
	for k, v := range config.Env {
		env[k] = v
	}

	secretKeyRefs := map[string]engine.SecretKeyRef{
		"ANTHROPIC_API_KEY": {SecretName: apiKeySecretName, Key: apiKeySecretKey},
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
			MountPath: "/home/robodev",
		},
		{
			// Provide a writable /tmp so Claude Code can create its subprocess
			// shell directories (e.g. /tmp/claude-<uid>/) for Bash tool execution.
			Name:      "tmp",
			MountPath: "/tmp",
		},
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

	if task.RepoURL != "" {
		b.WriteString("## Repository\n\n")
		b.WriteString(task.RepoURL)
		b.WriteString("\n\n")
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
		b.WriteString("1. Configure git globally:\n")
		b.WriteString("   ```\n")
		b.WriteString("   git config --global user.name \"RoboDev\"\n")
		b.WriteString("   git config --global user.email \"robodev@localhost\"\n")
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
		b.WriteString("2. Clone the repository to /workspace/repo:\n")
		b.WriteString("   ```\n")
		b.WriteString("   git clone --depth=1 ")
		b.WriteString(task.RepoURL)
		b.WriteString(" /workspace/repo\n")
		b.WriteString("   ```\n\n")
		b.WriteString("3. Work inside /workspace/repo to complete the task.\n\n")
		b.WriteString("4. When finished, write /workspace/result.json containing:\n")
		b.WriteString("   `{\"success\": true, \"summary\": \"<description of what was done>\"}`\n")
	} else {
		b.WriteString("Complete the task described above. Work in the /workspace directory.\n")
		b.WriteString("Write a result.json file to /workspace/result.json when finished.\n")
	}

	return b.String(), nil
}
