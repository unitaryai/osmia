// Package codex implements the ExecutionEngine interface for the OpenAI
// Codex CLI, translating tasks into execution specs that run Codex in
// fully autonomous mode inside Kubernetes Jobs.
//
// Codex does not support a hooks system, so guard rails are enforced via
// prompt-based instructions and command wrapping (see oss-plan section 6.3).
package codex

import (
	"fmt"
	"strings"

	"github.com/robodev-inc/robodev/pkg/engine"
)

const (
	// defaultImage is the container image used when no override is provided.
	defaultImage = "ghcr.io/robodev-inc/engine-codex:latest"

	// defaultTimeoutSeconds is the default active deadline (2 hours).
	defaultTimeoutSeconds = 7200

	// engineName is the unique identifier for this engine.
	engineName = "codex"

	// interfaceVersion is the version of the ExecutionEngine interface
	// this engine implements.
	interfaceVersion = 1

	// workspaceMountPath is where the workspace volume is mounted.
	workspaceMountPath = "/workspace"

	// configMountPath is where the configuration volume is mounted.
	configMountPath = "/config"

	// apiKeySecretName is the Kubernetes secret key for the OpenAI API key.
	apiKeySecretName = "openai-api-key"
)

// CodexEngine implements engine.ExecutionEngine for the OpenAI Codex CLI.
type CodexEngine struct{}

// New returns a new CodexEngine.
func New() *CodexEngine {
	return &CodexEngine{}
}

// Name returns the unique engine identifier.
func (e *CodexEngine) Name() string {
	return engineName
}

// InterfaceVersion returns the version of the ExecutionEngine interface
// this engine implements.
func (e *CodexEngine) InterfaceVersion() int {
	return interfaceVersion
}

// BuildExecutionSpec translates a task and engine configuration into an
// engine-agnostic ExecutionSpec for running Codex in full-auto mode.
// Because Codex lacks a hooks system, guard rails are embedded directly
// in the prompt text rather than enforced via pre-tool-use hooks.
func (e *CodexEngine) BuildExecutionSpec(task engine.Task, config engine.EngineConfig) (*engine.ExecutionSpec, error) {
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
		"codex",
		"--quiet",
		"--approval-mode", "full-auto",
		"--full-stdout",
		prompt,
	}

	env := map[string]string{
		"ROBODEV_TASK_ID":   task.ID,
		"ROBODEV_TICKET_ID": task.TicketID,
		"ROBODEV_REPO_URL":  task.RepoURL,
	}

	// Merge any extra environment variables from the engine config.
	for k, v := range config.Env {
		env[k] = v
	}

	secretEnv := map[string]string{
		"OPENAI_API_KEY": apiKeySecretName,
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

// BuildPrompt constructs the task prompt for Codex from the task's title,
// description, repository URL, and any additional metadata. Guard rails
// are appended directly to the prompt text because Codex does not support
// a hooks system for enforcing them at the tool level.
func (e *CodexEngine) BuildPrompt(task engine.Task) (string, error) {
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

	// Codex uses AGENTS.md for repository context instead of CLAUDE.md.
	b.WriteString("## Repository Context\n\n")
	b.WriteString("Refer to the AGENTS.md file in the repository root for coding conventions,\n")
	b.WriteString("project structure, and contribution guidelines.\n\n")

	b.WriteString("## Instructions\n\n")
	b.WriteString("Complete the task described above. Work in the /workspace directory.\n")
	b.WriteString("Write a result.json file to /workspace/result.json when finished.\n\n")

	// Guard rails embedded in prompt text (no hooks available).
	b.WriteString("## Guard Rails\n\n")
	b.WriteString("You MUST follow these rules strictly:\n")
	b.WriteString("- Do NOT execute destructive commands (e.g. rm -rf /, drop database, etc.)\n")
	b.WriteString("- Do NOT modify or read files matching sensitive patterns (*.env, **/secrets/**, *.key, *.pem)\n")
	b.WriteString("- Do NOT make network requests to external services other than the repository remote\n")
	b.WriteString("- Do NOT install packages or dependencies without explicit instructions to do so\n")
	b.WriteString("- Do NOT push commits directly; stage and commit changes locally only\n")

	return b.String(), nil
}
