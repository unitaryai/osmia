package aider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robodev-inc/robodev/pkg/engine"
)

// compile-time check that AiderEngine implements ExecutionEngine.
var _ engine.ExecutionEngine = (*AiderEngine)(nil)

func TestName(t *testing.T) {
	e := New()
	assert.Equal(t, "aider", e.Name())
}

func TestInterfaceVersion(t *testing.T) {
	e := New()
	assert.Equal(t, 1, e.InterfaceVersion())
}

func TestBuildExecutionSpec(t *testing.T) {
	baseTask := engine.Task{
		ID:          "task-1",
		TicketID:    "TICKET-42",
		Title:       "Fix login bug",
		Description: "The login page returns a 500 error when the password is empty.",
		RepoURL:     "https://github.com/org/repo",
	}

	tests := []struct {
		name    string
		task    engine.Task
		config  engine.EngineConfig
		opts    []Option
		check   func(t *testing.T, spec *engine.ExecutionSpec)
		wantErr bool
	}{
		{
			name: "default image when config image is empty",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, defaultImage, spec.Image)
			},
		},
		{
			name: "custom image from config",
			task: baseTask,
			config: engine.EngineConfig{
				Image:          "my-registry/aider:v2",
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "my-registry/aider:v2", spec.Image)
			},
		},
		{
			name: "command includes expected flags",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				require.True(t, len(spec.Command) >= 4, "command must have at least 4 elements")
				assert.Equal(t, "aider", spec.Command[0])
				assert.Equal(t, "--yes", spec.Command[1])
				assert.Equal(t, "--no-git", spec.Command[2])
				assert.Equal(t, "--message", spec.Command[3])
				// Last element is the prompt text.
				assert.Contains(t, spec.Command[4], "Fix login bug")
			},
		},
		{
			name: "environment variables are set",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "task-1", spec.Env["ROBODEV_TASK_ID"])
				assert.Equal(t, "TICKET-42", spec.Env["ROBODEV_TICKET_ID"])
				assert.Equal(t, "https://github.com/org/repo", spec.Env["ROBODEV_REPO_URL"])
			},
		},
		{
			name: "extra env from config is merged",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				Env: map[string]string{
					"CUSTOM_VAR": "custom_value",
				},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "custom_value", spec.Env["CUSTOM_VAR"])
				assert.Equal(t, "task-1", spec.Env["ROBODEV_TASK_ID"])
			},
		},
		{
			name: "default provider uses Anthropic API key",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, anthropicKeySecretName, spec.SecretEnv["ANTHROPIC_API_KEY"])
				_, hasOpenAI := spec.SecretEnv["OPENAI_API_KEY"]
				assert.False(t, hasOpenAI, "should not have OpenAI key with Anthropic provider")
			},
		},
		{
			name: "OpenAI provider uses OpenAI API key",
			task: baseTask,
			opts: []Option{WithProvider(ProviderOpenAI)},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, openAIKeySecretName, spec.SecretEnv["OPENAI_API_KEY"])
				_, hasAnthropic := spec.SecretEnv["ANTHROPIC_API_KEY"]
				assert.False(t, hasAnthropic, "should not have Anthropic key with OpenAI provider")
			},
		},
		{
			name: "volumes include workspace and config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				require.Len(t, spec.Volumes, 2)

				assert.Equal(t, "workspace", spec.Volumes[0].Name)
				assert.Equal(t, "/workspace", spec.Volumes[0].MountPath)
				assert.False(t, spec.Volumes[0].ReadOnly)

				assert.Equal(t, "config", spec.Volumes[1].Name)
				assert.Equal(t, "/config", spec.Volumes[1].MountPath)
				assert.True(t, spec.Volumes[1].ReadOnly)
			},
		},
		{
			name: "active deadline from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 1800,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, 1800, spec.ActiveDeadlineSeconds)
			},
		},
		{
			name:   "default deadline when config timeout is zero",
			task:   baseTask,
			config: engine.EngineConfig{},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, defaultTimeoutSeconds, spec.ActiveDeadlineSeconds)
			},
		},
		{
			name: "resource requests and limits from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds:   3600,
				ResourceRequests: engine.Resources{CPU: "500m", Memory: "512Mi"},
				ResourceLimits:   engine.Resources{CPU: "2", Memory: "4Gi"},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "500m", spec.ResourceRequests.CPU)
				assert.Equal(t, "512Mi", spec.ResourceRequests.Memory)
				assert.Equal(t, "2", spec.ResourceLimits.CPU)
				assert.Equal(t, "4Gi", spec.ResourceLimits.Memory)
			},
		},
		{
			name:    "empty task ID returns error",
			task:    engine.Task{Title: "test"},
			config:  engine.EngineConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(tt.opts...)
			spec, err := e.BuildExecutionSpec(tt.task, tt.config)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, spec)
			if tt.check != nil {
				tt.check(t, spec)
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name     string
		task     engine.Task
		contains []string
		wantErr  bool
	}{
		{
			name: "basic prompt with title and description",
			task: engine.Task{
				ID:          "task-1",
				Title:       "Fix login bug",
				Description: "The login page returns a 500 error.",
				RepoURL:     "https://github.com/org/repo",
			},
			contains: []string{
				"# Task: Fix login bug",
				"## Description",
				"The login page returns a 500 error.",
				"## Repository",
				"https://github.com/org/repo",
				"## Instructions",
			},
		},
		{
			name: "prompt references aider conventions",
			task: engine.Task{
				ID:    "task-2",
				Title: "Update feature",
			},
			contains: []string{
				".aider/conventions.md",
				".aider.conf.yml",
				"## Repository Context",
			},
		},
		{
			name: "prompt includes guard rails",
			task: engine.Task{
				ID:    "task-3",
				Title: "Refactor module",
			},
			contains: []string{
				"## Guard Rails",
				"destructive commands",
				"sensitive patterns",
				"network requests",
			},
		},
		{
			name: "prompt with labels",
			task: engine.Task{
				ID:     "task-4",
				Title:  "Add tests",
				Labels: []string{"testing", "quality"},
			},
			contains: []string{
				"## Labels",
				"testing, quality",
			},
		},
		{
			name: "prompt with metadata",
			task: engine.Task{
				ID:    "task-5",
				Title: "Update dependencies",
				Metadata: map[string]string{
					"priority": "high",
				},
			},
			contains: []string{
				"## Additional Context",
				"priority",
				"high",
			},
		},
		{
			name: "empty title returns error",
			task: engine.Task{
				ID: "task-6",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New()
			prompt, err := e.BuildPrompt(tt.task)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			for _, s := range tt.contains {
				assert.Contains(t, prompt, s)
			}
		})
	}
}

func TestWithProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider ModelProvider
		wantKey  string
	}{
		{
			name:     "anthropic provider",
			provider: ProviderAnthropic,
			wantKey:  "ANTHROPIC_API_KEY",
		},
		{
			name:     "openai provider",
			provider: ProviderOpenAI,
			wantKey:  "OPENAI_API_KEY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(WithProvider(tt.provider))
			task := engine.Task{
				ID:    "task-1",
				Title: "Test",
			}
			spec, err := e.BuildExecutionSpec(task, engine.EngineConfig{})
			require.NoError(t, err)
			_, ok := spec.SecretEnv[tt.wantKey]
			assert.True(t, ok, "expected secret env to contain %s", tt.wantKey)
		})
	}
}
