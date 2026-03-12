// Package opencode implements the ExecutionEngine interface for the
// OpenCode CLI, translating tasks into execution specs that run OpenCode
// inside Kubernetes Jobs.
//
// OpenCode uses AGENTS.md for repository context and coding conventions
// rather than CLAUDE.md. Guard rails are enforced via prompt-based
// instructions (see oss-plan section 6.3).
package opencode

import (
	"fmt"
	"strings"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// defaultImage is the container image used when no override is provided.
	defaultImage = "ghcr.io/unitaryai/engine-opencode:latest"

	// defaultTimeoutSeconds is the default active deadline (2 hours).
	defaultTimeoutSeconds = 7200

	// engineName is the unique identifier for this engine.
	engineName = "opencode"

	// interfaceVersion is the version of the ExecutionEngine interface
	// this engine implements.
	interfaceVersion = 1

	// workspaceMountPath is where the workspace volume is mounted.
	workspaceMountPath = "/workspace"

	// configMountPath is where the configuration volume is mounted.
	configMountPath = "/config"

	// anthropicKeySecretName is the Kubernetes secret key for the Anthropic API key.
	anthropicKeySecretName = "anthropic-api-key"

	// openAIKeySecretName is the Kubernetes secret key for the OpenAI API key.
	openAIKeySecretName = "openai-api-key"

	// googleKeySecretName is the Kubernetes secret key for the Google API key.
	googleKeySecretName = "google-api-key"
)

// ModelProvider indicates which LLM provider OpenCode should use.
type ModelProvider string

const (
	// ProviderAnthropic configures OpenCode to use the Anthropic API.
	ProviderAnthropic ModelProvider = "anthropic"

	// ProviderOpenAI configures OpenCode to use the OpenAI API.
	ProviderOpenAI ModelProvider = "openai"

	// ProviderGoogle configures OpenCode to use the Google API.
	ProviderGoogle ModelProvider = "google"
)

// OpenCodeEngine implements engine.ExecutionEngine for the OpenCode CLI.
type OpenCodeEngine struct {
	// provider determines which LLM provider OpenCode will use.
	// Defaults to ProviderAnthropic if not set.
	provider ModelProvider
}

// Option is a functional option for configuring the OpenCodeEngine.
type Option func(*OpenCodeEngine)

// WithProvider sets the model provider for the OpenCode engine.
func WithProvider(p ModelProvider) Option {
	return func(e *OpenCodeEngine) {
		e.provider = p
	}
}

// New returns a new OpenCodeEngine with the given options applied.
func New(opts ...Option) *OpenCodeEngine {
	e := &OpenCodeEngine{
		provider: ProviderAnthropic,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name returns the unique engine identifier.
func (e *OpenCodeEngine) Name() string {
	return engineName
}

// InterfaceVersion returns the version of the ExecutionEngine interface
// this engine implements.
func (e *OpenCodeEngine) InterfaceVersion() int {
	return interfaceVersion
}

// BuildExecutionSpec translates a task and engine configuration into an
// engine-agnostic ExecutionSpec for running OpenCode. Because OpenCode
// lacks a hooks system, guard rails are embedded directly in the prompt
// text.
func (e *OpenCodeEngine) BuildExecutionSpec(task engine.Task, config engine.EngineConfig) (*engine.ExecutionSpec, error) {
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
		"opencode",
		"--non-interactive",
		"--message", prompt,
	}

	env := map[string]string{
		"OSMIA_TASK_ID":   task.ID,
		"OSMIA_TICKET_ID": task.TicketID,
		"OSMIA_REPO_URL":  task.RepoURL,
	}

	// Merge any extra environment variables from the engine config.
	for k, v := range config.Env {
		env[k] = v
	}

	secretEnv := e.buildSecretEnv()

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

// buildSecretEnv returns the secret environment variable mapping based on
// the configured model provider.
func (e *OpenCodeEngine) buildSecretEnv() map[string]string {
	switch e.provider {
	case ProviderOpenAI:
		return map[string]string{
			"OPENAI_API_KEY": openAIKeySecretName,
		}
	case ProviderGoogle:
		return map[string]string{
			"GOOGLE_API_KEY": googleKeySecretName,
		}
	default:
		return map[string]string{
			"ANTHROPIC_API_KEY": anthropicKeySecretName,
		}
	}
}

// BuildPrompt constructs the task prompt for OpenCode from the task's
// title, description, and any additional metadata. Guard rails are
// appended directly to the prompt text because OpenCode does not support
// a hooks system for enforcing them at the tool level.
func (e *OpenCodeEngine) BuildPrompt(task engine.Task) (string, error) {
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

	// OpenCode uses AGENTS.md for repository context and coding conventions.
	b.WriteString("## Repository Context\n\n")
	b.WriteString("Refer to the AGENTS.md file in the repository for coding\n")
	b.WriteString("conventions, project structure, and contribution guidelines.\n")
	b.WriteString("OpenCode uses AGENTS.md for repository context and coding conventions.\n\n")

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
