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

	// apiKeySecretName is the Kubernetes secret key for the Anthropic API key.
	apiKeySecretName = "anthropic-api-key"
)

// ClaudeCodeEngine implements engine.ExecutionEngine for the Claude Code CLI.
type ClaudeCodeEngine struct{}

// New returns a new ClaudeCodeEngine.
func New() *ClaudeCodeEngine {
	return &ClaudeCodeEngine{}
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

	command := []string{
		"claude",
		"-p", prompt,
		"--output-format", "json",
		"--max-turns", strconv.Itoa(defaultMaxTurns),
		"--dangerously-skip-permissions",
	}

	env := map[string]string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"ROBODEV_TASK_ID":                          task.ID,
		"ROBODEV_TICKET_ID":                        task.TicketID,
		"ROBODEV_REPO_URL":                         task.RepoURL,
	}

	// Merge any extra environment variables from the engine config.
	for k, v := range config.Env {
		env[k] = v
	}

	secretEnv := map[string]string{
		"ANTHROPIC_API_KEY": apiKeySecretName,
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
	}

	spec := &engine.ExecutionSpec{
		Image:                 image,
		Command:               command,
		Env:                   env,
		SecretEnv:             secretEnv,
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
	b.WriteString("Complete the task described above. Work in the /workspace directory.\n")
	b.WriteString("Write a result.json file to /workspace/result.json when finished.\n")

	return b.String(), nil
}
