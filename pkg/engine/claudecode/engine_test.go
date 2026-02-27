package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/pkg/engine"
)

// compile-time check that ClaudeCodeEngine implements ExecutionEngine.
var _ engine.ExecutionEngine = (*ClaudeCodeEngine)(nil)

func TestName(t *testing.T) {
	e := New()
	assert.Equal(t, "claude-code", e.Name())
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
				Image:          "my-registry/claude-code:v2",
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "my-registry/claude-code:v2", spec.Image)
			},
		},
		{
			name: "command includes expected flags",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				require.True(t, len(spec.Command) >= 2, "command must have at least 2 elements")
				assert.Equal(t, "claude", spec.Command[0])
				assert.Equal(t, "-p", spec.Command[1])
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "json")
				assert.Contains(t, spec.Command, "--max-turns")
				assert.Contains(t, spec.Command, "50")
				assert.Contains(t, spec.Command, "--dangerously-skip-permissions")
			},
		},
		{
			name: "environment variables are set",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "1", spec.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"])
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
				// Core env vars should still be present.
				assert.Equal(t, "1", spec.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"])
			},
		},
		{
			name: "secret env contains API key reference",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, apiKeySecretName, spec.SecretEnv["ANTHROPIC_API_KEY"])
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
			e := New()
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
			name: "prompt with metadata",
			task: engine.Task{
				ID:    "task-2",
				Title: "Update dependencies",
				Metadata: map[string]string{
					"priority": "high",
				},
			},
			contains: []string{
				"# Task: Update dependencies",
				"## Additional Context",
				"priority",
				"high",
			},
		},
		{
			name: "prompt with labels",
			task: engine.Task{
				ID:     "task-3",
				Title:  "Add tests",
				Labels: []string{"testing", "quality"},
			},
			contains: []string{
				"## Labels",
				"testing, quality",
			},
		},
		{
			name: "empty description is omitted",
			task: engine.Task{
				ID:      "task-4",
				Title:   "Simple task",
				RepoURL: "https://github.com/org/repo",
			},
			contains: []string{
				"# Task: Simple task",
				"## Repository",
				"## Instructions",
			},
		},
		{
			name: "empty title returns error",
			task: engine.Task{
				ID: "task-5",
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

func TestGenerateHooksConfig(t *testing.T) {
	tests := []struct {
		name                string
		blockedCommands     []string
		blockedFilePatterns []string
		check               func(t *testing.T, data []byte)
	}{
		{
			name:                "default hooks with empty lists",
			blockedCommands:     nil,
			blockedFilePatterns: nil,
			check: func(t *testing.T, data []byte) {
				var cfg hooksConfig
				require.NoError(t, json.Unmarshal(data, &cfg))

				require.Len(t, cfg.Hooks.PreToolUse, 2)
				assert.Equal(t, "Bash", cfg.Hooks.PreToolUse[0].Matcher)
				assert.Equal(t, "Write|Edit", cfg.Hooks.PreToolUse[1].Matcher)

				require.Len(t, cfg.Hooks.PostToolUse, 1)
				assert.Contains(t, cfg.Hooks.PostToolUse[0].Hooks[0].Command, "heartbeat.sh")

				require.Len(t, cfg.Hooks.Stop, 1)
				assert.Contains(t, cfg.Hooks.Stop[0].Hooks[0].Command, "on-complete.sh")
			},
		},
		{
			name:                "blocked commands are included in script arguments",
			blockedCommands:     []string{"rm -rf", "curl", "wget"},
			blockedFilePatterns: nil,
			check: func(t *testing.T, data []byte) {
				var cfg hooksConfig
				require.NoError(t, json.Unmarshal(data, &cfg))

				cmd := cfg.Hooks.PreToolUse[0].Hooks[0].Command
				assert.Contains(t, cmd, "block-dangerous-commands.sh")
				assert.Contains(t, cmd, "rm -rf")
				assert.Contains(t, cmd, "curl")
				assert.Contains(t, cmd, "wget")
			},
		},
		{
			name:                "blocked file patterns are included in script arguments",
			blockedCommands:     nil,
			blockedFilePatterns: []string{"*.env", "**/secrets/**"},
			check: func(t *testing.T, data []byte) {
				var cfg hooksConfig
				require.NoError(t, json.Unmarshal(data, &cfg))

				cmd := cfg.Hooks.PreToolUse[1].Hooks[0].Command
				assert.Contains(t, cmd, "block-sensitive-files.sh")
				assert.Contains(t, cmd, "*.env")
				assert.Contains(t, cmd, "**/secrets/**")
			},
		},
		{
			name:                "output is valid JSON",
			blockedCommands:     []string{"rm"},
			blockedFilePatterns: []string{"*.key"},
			check: func(t *testing.T, data []byte) {
				assert.True(t, json.Valid(data), "output must be valid JSON")
			},
		},
		{
			name:                "all hook types are command type",
			blockedCommands:     nil,
			blockedFilePatterns: nil,
			check: func(t *testing.T, data []byte) {
				var cfg hooksConfig
				require.NoError(t, json.Unmarshal(data, &cfg))

				for _, mg := range cfg.Hooks.PreToolUse {
					for _, h := range mg.Hooks {
						assert.Equal(t, "command", h.Type)
					}
				}
				for _, mg := range cfg.Hooks.PostToolUse {
					for _, h := range mg.Hooks {
						assert.Equal(t, "command", h.Type)
					}
				}
				for _, mg := range cfg.Hooks.Stop {
					for _, h := range mg.Hooks {
						assert.Equal(t, "command", h.Type)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := GenerateHooksConfig(tt.blockedCommands, tt.blockedFilePatterns)
			require.NoError(t, err)
			require.NotEmpty(t, data)
			if tt.check != nil {
				tt.check(t, data)
			}
		})
	}
}
