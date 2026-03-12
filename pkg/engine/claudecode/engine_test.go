package claudecode

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
)

// compile-time check that ClaudeCodeEngine implements ExecutionEngine.
var _ engine.ExecutionEngine = (*ClaudeCodeEngine)(nil)

// stubSessionStore is a minimal SessionStore used in unit tests.
type stubSessionStore struct{}

func (s *stubSessionStore) Prepare(_ context.Context, _ string) error {
	return nil
}

func (s *stubSessionStore) VolumeMounts(_ string) []engine.VolumeMount {
	return []engine.VolumeMount{
		{Name: "stub-session", MountPath: "/session", PVCName: "stub-pvc"},
	}
}

func (s *stubSessionStore) Env(_, _ string) map[string]string {
	return map[string]string{
		"CLAUDE_CONFIG_DIR": "/session/claude",
	}
}

func (s *stubSessionStore) Cleanup(_ context.Context, _ string) error {
	return nil
}

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
		opts    []Option
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
				assert.Equal(t, "setup-claude.sh", spec.Command[0])
				assert.Equal(t, "-p", spec.Command[1])
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--verbose")
				assert.Contains(t, spec.Command, "--max-turns")
				assert.Contains(t, spec.Command, "50")
				assert.Contains(t, spec.Command, "--dangerously-skip-permissions")
				assert.Contains(t, spec.Command, "--mcp-config")
				assert.Contains(t, spec.Command, "/workspace/.mcp.json")
			},
		},
		{
			name: "WithMaxTurns overrides default",
			opts: []Option{WithMaxTurns(200)},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				idx := -1
				for i, s := range spec.Command {
					if s == "--max-turns" {
						idx = i
						break
					}
				}
				require.NotEqual(t, -1, idx, "--max-turns flag must be present")
				require.Less(t, idx+1, len(spec.Command), "--max-turns must be followed by a value")
				assert.Equal(t, "200", spec.Command[idx+1])
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
				assert.Equal(t, "task-1", spec.Env["OSMIA_TASK_ID"])
				assert.Equal(t, "TICKET-42", spec.Env["OSMIA_TICKET_ID"])
				assert.Equal(t, "https://github.com/org/repo", spec.Env["OSMIA_REPO_URL"])
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
			name: "secret key ref injects ANTHROPIC_API_KEY from correct secret and key",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				ref, ok := spec.SecretKeyRefs["ANTHROPIC_API_KEY"]
				require.True(t, ok, "ANTHROPIC_API_KEY must be present in SecretKeyRefs")
				assert.Equal(t, apiKeySecretName, ref.SecretName)
				assert.Equal(t, apiKeySecretKey, ref.Key)
			},
		},
		{
			name: "volumes include workspace, config, and home",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				require.Len(t, spec.Volumes, 4)

				assert.Equal(t, "workspace", spec.Volumes[0].Name)
				assert.Equal(t, "/workspace", spec.Volumes[0].MountPath)
				assert.False(t, spec.Volumes[0].ReadOnly)

				assert.Equal(t, "config", spec.Volumes[1].Name)
				assert.Equal(t, "/config", spec.Volumes[1].MountPath)
				assert.True(t, spec.Volumes[1].ReadOnly)

				assert.Equal(t, "home", spec.Volumes[2].Name)
				assert.Equal(t, "/home/osmia", spec.Volumes[2].MountPath)
				assert.False(t, spec.Volumes[2].ReadOnly)

				assert.Equal(t, "tmp", spec.Volumes[3].Name)
				assert.Equal(t, "/tmp", spec.Volumes[3].MountPath)
				assert.False(t, spec.Volumes[3].ReadOnly)
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

		// --- New flag combination tests ---

		{
			name: "json schema from config switches to stream-json output",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				JSONSchema:     `{"type":"object"}`,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.NotContains(t, spec.Command, "json")
				assert.Contains(t, spec.Command, "--json-schema")
				assert.Contains(t, spec.Command, `{"type":"object"}`)
			},
		},
		{
			name: "json schema from engine option switches to stream-json output",
			opts: []Option{WithJSONSchema(DefaultTaskResultSchema)},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--json-schema")
				assert.Contains(t, spec.Command, DefaultTaskResultSchema)
			},
		},
		{
			name: "config json schema overrides engine option",
			opts: []Option{WithJSONSchema(`{"type":"string"}`)},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				JSONSchema:     `{"type":"object"}`,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--json-schema")
				assert.Contains(t, spec.Command, `{"type":"object"}`)
				assert.NotContains(t, spec.Command, `{"type":"string"}`)
			},
		},
		{
			name: "no json schema omits --json-schema flag",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				// stream-json is always used; --json-schema is only added when a
				// schema is provided.
				assert.Contains(t, spec.Command, "stream-json")
				assert.NotContains(t, spec.Command, "--json-schema")
			},
		},
		{
			name: "fallback model from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				FallbackModel:  "haiku",
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--fallback-model")
				assert.Contains(t, spec.Command, "haiku")
			},
		},
		{
			name: "fallback model from engine option",
			opts: []Option{WithFallbackModel("sonnet")},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--fallback-model")
				assert.Contains(t, spec.Command, "sonnet")
			},
		},
		{
			name: "config fallback model overrides engine option",
			opts: []Option{WithFallbackModel("sonnet")},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				FallbackModel:  "haiku",
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--fallback-model")
				assert.Contains(t, spec.Command, "haiku")
				assert.NotContains(t, spec.Command, "sonnet")
			},
		},
		{
			name: "no fallback model omits flag",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--fallback-model")
			},
		},
		{
			name: "--no-session-persistence flag is never emitted",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--no-session-persistence")
			},
		},
		{
			name: "session store with empty SessionID adds --session-id flag",
			opts: []Option{WithSessionStore(&stubSessionStore{})},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--session-id")
				assert.NotContains(t, spec.Command, "--resume")
				// Session ID must be a valid UUID.
				idx := -1
				for i, s := range spec.Command {
					if s == "--session-id" {
						idx = i
						break
					}
				}
				require.NotEqual(t, -1, idx)
				require.Less(t, idx+1, len(spec.Command))
				assert.NotEmpty(t, spec.Command[idx+1])
			},
		},
		{
			name: "session store with SessionID set adds --resume flag",
			opts: []Option{WithSessionStore(&stubSessionStore{})},
			task: engine.Task{
				ID:        "task-1",
				TicketID:  "TICKET-42",
				Title:     "Fix login bug",
				RepoURL:   "https://github.com/org/repo",
				SessionID: "abc-123-session",
			},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--resume")
				assert.Contains(t, spec.Command, "abc-123-session")
				assert.NotContains(t, spec.Command, "--session-id")
			},
		},
		{
			name: "session store volumes are merged into spec",
			opts: []Option{WithSessionStore(&stubSessionStore{})},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				found := false
				for _, v := range spec.Volumes {
					if v.Name == "stub-session" {
						found = true
					}
				}
				assert.True(t, found, "stub session volume should be present")
			},
		},
		{
			name: "session store env vars are merged into spec",
			opts: []Option{WithSessionStore(&stubSessionStore{})},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Equal(t, "/session/claude", spec.Env["CLAUDE_CONFIG_DIR"])
			},
		},
		{
			name: "no session store means no session flags",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--session-id")
				assert.NotContains(t, spec.Command, "--resume")
			},
		},
		{
			name: "append system prompt from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds:     3600,
				AppendSystemPrompt: "Never modify production databases.",
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--append-system-prompt")
				assert.Contains(t, spec.Command, "Never modify production databases.")
			},
		},
		{
			name: "append system prompt omitted when empty",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--append-system-prompt")
			},
		},
		{
			name: "tool whitelist from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolWhitelist:  []string{"Read", "Write", "Bash"},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--allowedTools")
				assert.Contains(t, spec.Command, "Read,Write,Bash")
			},
		},
		{
			name: "tool whitelist from engine option",
			opts: []Option{WithToolWhitelist([]string{"Read", "Grep"})},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--allowedTools")
				assert.Contains(t, spec.Command, "Read,Grep")
			},
		},
		{
			name: "config tool whitelist overrides engine option",
			opts: []Option{WithToolWhitelist([]string{"Read", "Grep"})},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolWhitelist:  []string{"Bash", "Write"},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--allowedTools")
				assert.Contains(t, spec.Command, "Bash,Write")
				assert.NotContains(t, spec.Command, "Read,Grep")
			},
		},
		{
			name: "empty tool whitelist omits flag",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--allowedTools")
			},
		},
		{
			name: "tool blacklist from config",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolBlacklist:  []string{"Bash", "NotebookEdit"},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--disallowedTools")
				assert.Contains(t, spec.Command, "Bash,NotebookEdit")
			},
		},
		{
			name: "empty tool blacklist omits flag",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.NotContains(t, spec.Command, "--disallowedTools")
			},
		},
		// --- Skill injection tests ---

		{
			name: "inline skill sets CLAUDE_SKILL_INLINE env var",
			opts: []Option{
				WithSkills([]Skill{
					{Name: "create-changelog", Inline: "# Create Changelog\n\nDo the thing."},
				}),
			},
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				key := "CLAUDE_SKILL_INLINE_CREATE_CHANGELOG"
				assert.Contains(t, spec.Env, key, "inline skill env var must be present")
				assert.NotEmpty(t, spec.Env[key], "inline skill env var must not be empty")
			},
		},
		{
			name: "path skill sets CLAUDE_SKILL_PATH env var",
			opts: []Option{
				WithSkills([]Skill{
					{Name: "review", Path: "/opt/osmia/skills/review.md"},
				}),
			},
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				key := "CLAUDE_SKILL_PATH_REVIEW"
				require.Contains(t, spec.Env, key, "path skill env var must be present")
				assert.Equal(t, "/opt/osmia/skills/review.md", spec.Env[key])
			},
		},
		{
			name:   "no skills produces no CLAUDE_SKILL_ env vars",
			opts:   nil,
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				for k := range spec.Env {
					assert.False(t, len(k) >= 13 && k[:13] == "CLAUDE_SKILL_",
						"unexpected skill env var: %s", k)
				}
			},
		},

		// --- ConfigMap skill volume tests ---

		{
			name: "configmap skill adds volume and env var",
			opts: []Option{
				WithSkills([]Skill{
					{Name: "deploy", ConfigMap: "deploy-cm"},
				}),
			},
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				// Should have CLAUDE_SKILL_PATH pointing to mount.
				assert.Equal(t, "/skills/deploy.md", spec.Env["CLAUDE_SKILL_PATH_DEPLOY"])
				// Should have extra ConfigMap volume.
				found := false
				for _, v := range spec.Volumes {
					if v.Name == "skill-deploy" {
						found = true
						assert.Equal(t, "/skills/deploy.md", v.MountPath)
						assert.Equal(t, "deploy-cm", v.ConfigMapName)
						assert.True(t, v.ReadOnly)
					}
				}
				assert.True(t, found, "expected skill-deploy volume")
			},
		},

		// --- Sub-agent tests ---

		{
			name: "inline sub-agent adds --agents flag",
			opts: []Option{
				WithSubAgents([]SubAgent{
					{Name: "reviewer", Description: "Reviews code", Model: "haiku"},
				}),
			},
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--agents")
				// Find the agents JSON.
				for i, c := range spec.Command {
					if c == "--agents" {
						var m map[string]any
						require.NoError(t, json.Unmarshal([]byte(spec.Command[i+1]), &m))
						assert.Contains(t, m, "reviewer")
						break
					}
				}
			},
		},
		{
			name: "configmap sub-agent adds volume and env var",
			opts: []Option{
				WithSubAgents([]SubAgent{
					{Name: "architect", Description: "System architect", ConfigMap: "architect-cm"},
				}),
			},
			task:   baseTask,
			config: engine.EngineConfig{TimeoutSeconds: 3600},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				// Should NOT have --agents flag (ConfigMap agents are file-based).
				assert.NotContains(t, spec.Command, "--agents")
				// Should have env var.
				assert.Equal(t, "/subagents/architect.md", spec.Env["CLAUDE_SUBAGENT_PATH_ARCHITECT"])
				// Should have ConfigMap volume.
				found := false
				for _, v := range spec.Volumes {
					if v.Name == "subagent-architect" {
						found = true
						assert.Equal(t, "/subagents/architect.md", v.MountPath)
						assert.Equal(t, "architect-cm", v.ConfigMapName)
					}
				}
				assert.True(t, found, "expected subagent-architect volume")
			},
		},

		// --- Streaming output tests ---

		{
			name: "streaming enabled without json schema uses stream-json and verbose",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds:   3600,
				StreamingEnabled: true,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--verbose")
				assert.NotContains(t, spec.Command, "--json-schema")
			},
		},
		{
			name: "streaming enabled with json schema uses stream-json verbose and schema",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds:   3600,
				StreamingEnabled: true,
				JSONSchema:       `{"type":"object"}`,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--verbose")
				assert.Contains(t, spec.Command, "--json-schema")
				assert.Contains(t, spec.Command, `{"type":"object"}`)
			},
		},
		{
			name: "always uses stream-json and verbose",
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "--output-format")
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--verbose")
			},
		},

		{
			name: "all flags combined",
			opts: []Option{
				WithFallbackModel("default-fallback"),
				WithSessionStore(&stubSessionStore{}),
			},
			task: baseTask,
			config: engine.EngineConfig{
				TimeoutSeconds:     3600,
				FallbackModel:      "haiku",
				JSONSchema:         DefaultTaskResultSchema,
				AppendSystemPrompt: "Be careful.",
				ToolWhitelist:      []string{"Read", "Write"},
				ToolBlacklist:      []string{"Bash"},
			},
			check: func(t *testing.T, spec *engine.ExecutionSpec) {
				assert.Contains(t, spec.Command, "stream-json")
				assert.Contains(t, spec.Command, "--json-schema")
				assert.Contains(t, spec.Command, "--verbose")
				assert.Contains(t, spec.Command, "--fallback-model")
				assert.Contains(t, spec.Command, "haiku")
				assert.NotContains(t, spec.Command, "--no-session-persistence")
				assert.Contains(t, spec.Command, "--session-id")
				assert.Contains(t, spec.Command, "--append-system-prompt")
				assert.Contains(t, spec.Command, "Be careful.")
				assert.Contains(t, spec.Command, "--allowedTools")
				assert.Contains(t, spec.Command, "Read,Write")
				assert.Contains(t, spec.Command, "--disallowedTools")
				assert.Contains(t, spec.Command, "Bash")
			},
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

func TestFunctionalOptions(t *testing.T) {
	tests := []struct {
		name  string
		opts  []Option
		check func(t *testing.T, e *ClaudeCodeEngine)
	}{
		{
			name: "no options leaves defaults",
			opts: nil,
			check: func(t *testing.T, e *ClaudeCodeEngine) {
				assert.Empty(t, e.fallbackModel)
				assert.Empty(t, e.toolWhitelist)
				assert.Empty(t, e.jsonSchema)
			},
		},
		{
			name: "WithFallbackModel sets fallback",
			opts: []Option{WithFallbackModel("haiku")},
			check: func(t *testing.T, e *ClaudeCodeEngine) {
				assert.Equal(t, "haiku", e.fallbackModel)
			},
		},
		{
			name: "WithToolWhitelist sets whitelist",
			opts: []Option{WithToolWhitelist([]string{"Read", "Write"})},
			check: func(t *testing.T, e *ClaudeCodeEngine) {
				assert.Equal(t, []string{"Read", "Write"}, e.toolWhitelist)
			},
		},
		{
			name: "WithJSONSchema sets schema",
			opts: []Option{WithJSONSchema(DefaultTaskResultSchema)},
			check: func(t *testing.T, e *ClaudeCodeEngine) {
				assert.Equal(t, DefaultTaskResultSchema, e.jsonSchema)
			},
		},
		{
			name: "multiple options compose",
			opts: []Option{
				WithFallbackModel("sonnet"),
				WithToolWhitelist([]string{"Bash"}),
				WithJSONSchema(`{"type":"object"}`),
			},
			check: func(t *testing.T, e *ClaudeCodeEngine) {
				assert.Equal(t, "sonnet", e.fallbackModel)
				assert.Equal(t, []string{"Bash"}, e.toolWhitelist)
				assert.Equal(t, `{"type":"object"}`, e.jsonSchema)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(tt.opts...)
			tt.check(t, e)
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
				"git config --global user.name",
				"git clone --depth=1 https://github.com/org/repo /workspace/repo",
				"/workspace/repo",
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
				"git clone --depth=1 https://github.com/org/repo /workspace/repo",
			},
		},
		{
			name: "empty title returns error",
			task: engine.Task{
				ID: "task-5",
			},
			wantErr: true,
		},
		{
			name: "base prompt prescribes deterministic branch name and push instruction",
			task: engine.Task{
				ID:       "task-6",
				TicketID: "TICKET-99",
				Title:    "Add feature",
				RepoURL:  "https://github.com/org/repo",
			},
			contains: []string{
				"osmia/TICKET-99",
				"git checkout -b osmia/TICKET-99",
				"git push origin osmia/TICKET-99",
				"Commit and push your changes to that branch at logical checkpoints",
				"\"branch_name\": \"osmia/TICKET-99\"",
			},
		},
		{
			name: "continuation section present when PriorBranchName is set",
			task: engine.Task{
				ID:              "task-7",
				TicketID:        "TICKET-99",
				Title:           "Add feature",
				RepoURL:         "https://github.com/org/repo",
				PriorBranchName: "osmia/TICKET-99",
			},
			contains: []string{
				"## Continuation",
				"osmia/TICKET-99",
				"git clone --branch osmia/TICKET-99 --depth=50 https://github.com/org/repo /workspace/repo",
				"git log --oneline -20",
				"Do not redo work that is already committed.",
			},
		},
		{
			name: "no continuation section when PriorBranchName is empty",
			task: engine.Task{
				ID:      "task-8",
				TicketID: "TICKET-88",
				Title:   "Add feature",
				RepoURL: "https://github.com/org/repo",
			},
			contains: []string{
				"git clone --depth=1 https://github.com/org/repo /workspace/repo",
			},
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

func TestBuildPrompt_NoContinuationSectionWhenSessionIDSet(t *testing.T) {
	// When a session is being resumed, --resume handles context so the
	// Continuation section must be suppressed to avoid confusing the agent.
	e := New()
	prompt, err := e.BuildPrompt(engine.Task{
		ID:              "task-1",
		TicketID:        "TICKET-1",
		Title:           "Fix bug",
		RepoURL:         "https://github.com/org/repo",
		PriorBranchName: "osmia/TICKET-1",
		SessionID:       "some-session-id",
	})
	require.NoError(t, err)
	assert.NotContains(t, prompt, "## Continuation")
}

func TestBuildPrompt_NoContinuationSectionWhenEmpty(t *testing.T) {
	e := New()
	prompt, err := e.BuildPrompt(engine.Task{
		ID:      "task-1",
		TicketID: "TICKET-1",
		Title:   "Fix bug",
		RepoURL: "https://github.com/org/repo",
	})
	require.NoError(t, err)
	assert.NotContains(t, prompt, "## Continuation")
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
