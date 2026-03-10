// Package cline implements the ExecutionEngine interface for the Cline
// CLI, translating tasks into execution specs that run Cline inside
// Kubernetes Jobs.
//
// Cline uses .clinerules for project-specific instructions rather than
// CLAUDE.md. Guard rails are enforced via prompt-based instructions
// (see oss-plan section 6.3). Cline optionally supports MCP (Model
// Context Protocol) integration via the --mcp flag.
package cline

import (
	"fmt"
	"strings"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// defaultImage is the container image used when no override is provided.
	defaultImage = "ghcr.io/unitaryai/engine-cline:latest"

	// defaultTimeoutSeconds is the default active deadline (2 hours).
	defaultTimeoutSeconds = 7200

	// engineName is the unique identifier for this engine.
	engineName = "cline"

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

	// awsAccessKeySecretName is the Kubernetes secret key for the AWS access key ID.
	awsAccessKeySecretName = "aws-access-key-id"

	// awsSecretKeySecretName is the Kubernetes secret key for the AWS secret access key.
	awsSecretKeySecretName = "aws-secret-access-key"
)

// ModelProvider indicates which LLM provider Cline should use.
type ModelProvider string

const (
	// ProviderAnthropic configures Cline to use the Anthropic API.
	ProviderAnthropic ModelProvider = "anthropic"

	// ProviderOpenAI configures Cline to use the OpenAI API.
	ProviderOpenAI ModelProvider = "openai"

	// ProviderGoogle configures Cline to use the Google API.
	ProviderGoogle ModelProvider = "google"

	// ProviderBedrock configures Cline to use AWS Bedrock.
	ProviderBedrock ModelProvider = "bedrock"
)

// ClineEngine implements engine.ExecutionEngine for the Cline CLI.
type ClineEngine struct {
	// provider determines which LLM provider Cline will use.
	// Defaults to ProviderAnthropic if not set.
	provider ModelProvider

	// mcpEnabled controls whether MCP (Model Context Protocol) support
	// is enabled. When true, the --mcp flag is appended to the command.
	mcpEnabled bool
}

// Option is a functional option for configuring the ClineEngine.
type Option func(*ClineEngine)

// WithProvider sets the model provider for the Cline engine.
func WithProvider(p ModelProvider) Option {
	return func(e *ClineEngine) {
		e.provider = p
	}
}

// WithMCPEnabled controls whether MCP (Model Context Protocol) support
// is enabled. When true, the --mcp flag is appended to the Cline command.
func WithMCPEnabled(enabled bool) Option {
	return func(e *ClineEngine) {
		e.mcpEnabled = enabled
	}
}

// New returns a new ClineEngine with the given options applied.
func New(opts ...Option) *ClineEngine {
	e := &ClineEngine{
		provider: ProviderAnthropic,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name returns the unique engine identifier.
func (e *ClineEngine) Name() string {
	return engineName
}

// InterfaceVersion returns the version of the ExecutionEngine interface
// this engine implements.
func (e *ClineEngine) InterfaceVersion() int {
	return interfaceVersion
}

// BuildExecutionSpec translates a task and engine configuration into an
// engine-agnostic ExecutionSpec for running Cline. Because Cline lacks a
// hooks system, guard rails are embedded directly in the prompt text.
func (e *ClineEngine) BuildExecutionSpec(task engine.Task, config engine.EngineConfig) (*engine.ExecutionSpec, error) {
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
		"cline",
		"--headless",
		"--task", prompt,
		"--output-format", "json",
	}

	if e.mcpEnabled {
		command = append(command, "--mcp")
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
func (e *ClineEngine) buildSecretEnv() map[string]string {
	switch e.provider {
	case ProviderOpenAI:
		return map[string]string{
			"OPENAI_API_KEY": openAIKeySecretName,
		}
	case ProviderGoogle:
		return map[string]string{
			"GOOGLE_API_KEY": googleKeySecretName,
		}
	case ProviderBedrock:
		return map[string]string{
			"AWS_ACCESS_KEY_ID":     awsAccessKeySecretName,
			"AWS_SECRET_ACCESS_KEY": awsSecretKeySecretName,
		}
	default:
		return map[string]string{
			"ANTHROPIC_API_KEY": anthropicKeySecretName,
		}
	}
}

// BuildPrompt constructs the task prompt for Cline from the task's title,
// description, and any additional metadata. Guard rails are appended
// directly to the prompt text because Cline does not support a hooks
// system for enforcing them at the tool level.
func (e *ClineEngine) BuildPrompt(task engine.Task) (string, error) {
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

	// Cline uses .clinerules for project-specific instructions.
	b.WriteString("## Repository Context\n\n")
	b.WriteString("Refer to the .clinerules file in the repository for project-specific\n")
	b.WriteString("instructions, coding conventions, and contribution guidelines.\n")
	b.WriteString("Cline uses .clinerules for project-specific instructions.\n\n")

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
