package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    *Config
		wantErr bool
	}{
		{
			name: "valid minimal config",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: &Config{
				Ticketing: TicketingConfig{Backend: "github"},
				Secrets:   SecretsConfig{Backend: "env"},
				Engines:   EnginesConfig{Default: "claude-code"},
				GuardRails: GuardRailsConfig{
					MaxCostPerJob:         5.0,
					MaxConcurrentJobs:     10,
					MaxJobDurationMinutes: 60,
				},
				Routing: RoutingConfig{EpsilonGreedy: 0.1},
			},
		},
		{
			name: "config with notifications and plugin health",
			yaml: `
ticketing:
  backend: jira
  config:
    url: https://example.atlassian.net
notifications:
  channels:
    - backend: slack
      config:
        webhook_url: https://hooks.slack.com/example
secrets:
  backend: aws-secrets-manager
engines:
  default: codex
guardrails:
  max_cost_per_job: 10.0
  max_concurrent_jobs: 5
  max_job_duration_minutes: 120
  allowed_repos:
    - github.com/example/repo
  blocked_file_patterns:
    - "*.env"
    - "secrets/**"
  allowed_task_types:
    - dependency-update
    - bug-fix
plugin_health:
  max_plugin_restarts: 3
  restart_backoff:
    - 1
    - 5
    - 30
  critical_plugins:
    - ticketing
    - notifications
`,
			want: &Config{
				Ticketing: TicketingConfig{
					Backend: "jira",
					Config:  map[string]any{"url": "https://example.atlassian.net"},
				},
				Notifications: NotificationsConfig{
					Channels: []ChannelConfig{
						{
							Backend: "slack",
							Config:  map[string]any{"webhook_url": "https://hooks.slack.com/example"},
						},
					},
				},
				Secrets: SecretsConfig{Backend: "aws-secrets-manager"},
				Engines: EnginesConfig{Default: "codex"},
				GuardRails: GuardRailsConfig{
					MaxCostPerJob:         10.0,
					MaxConcurrentJobs:     5,
					MaxJobDurationMinutes: 120,
					AllowedRepos:          []string{"github.com/example/repo"},
					BlockedFilePatterns:   []string{"*.env", "secrets/**"},
					AllowedTaskTypes:      []string{"dependency-update", "bug-fix"},
				},
				PluginHealth: PluginHealthConfig{
					MaxPluginRestarts: 3,
					RestartBackoff:    []int{1, 5, 30},
					CriticalPlugins:   []string{"ticketing", "notifications"},
				},
				Routing: RoutingConfig{EpsilonGreedy: 0.1},
			},
		},
		{
			name:    "invalid yaml",
			yaml:    ":\tinvalid: yaml: content",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write the YAML to a temporary file.
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoad_RejectsNonStringLocalSeedFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
	err := os.WriteFile(tmp, []byte(`
ticketing:
  backend: local
  config:
    store_path: /tmp/local-ticketing.db
    seed_file: 123
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`), 0o600)
	require.NoError(t, err)

	_, err = Load(tmp)
	require.ErrorContains(t, err, "ticketing.config.seed_file must be a string")
}

// TestLoad_ClaudeCodeModels pins the YAML names of the Bedrock models block.
func TestLoad_ClaudeCodeModels(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
	err := os.WriteFile(tmp, []byte(`
engines:
  default: claude-code
  claude-code:
    auth:
      method: bedrock
      bedrock_region: example-region-1
    models:
      main: example-main-model
      opus: example-opus-model
      sonnet: example-sonnet-model
      haiku: example-haiku-model
`), 0o600)
	require.NoError(t, err)

	got, err := Load(tmp)

	require.NoError(t, err)
	require.NotNil(t, got.Engines.ClaudeCode)
	assert.Equal(t, ClaudeCodeModels{
		Main:   "example-main-model",
		Opus:   "example-opus-model",
		Sonnet: "example-sonnet-model",
		Haiku:  "example-haiku-model",
	}, got.Engines.ClaudeCode.Models)
}

func TestLoad_TaskProfiles(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantProfiles map[string]TaskProfileConfig
	}{
		{
			name: "task profiles with workflow modes",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
  task_profiles:
    bug_fix:
      workflow: tdd
    feature:
      workflow: tdd
      tool_whitelist:
        - go test
        - make lint
    review:
      workflow: review-first
`,
			wantProfiles: map[string]TaskProfileConfig{
				"bug_fix": {
					Workflow: "tdd",
				},
				"feature": {
					Workflow:      "tdd",
					ToolWhitelist: []string{"go test", "make lint"},
				},
				"review": {
					Workflow: "review-first",
				},
			},
		},
		{
			name: "empty task profiles",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantProfiles: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			require.NoError(t, err)

			assert.Equal(t, tt.wantProfiles, got.GuardRails.TaskProfiles)
		})
	}
}

func TestLoad_StreamingConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want StreamingConfig
	}{
		{
			name: "streaming enabled with live notifications",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
streaming:
  enabled: true
  live_notifications: true
`,
			want: StreamingConfig{
				Enabled:           true,
				LiveNotifications: true,
			},
		},
		{
			name: "streaming disabled by default",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: StreamingConfig{},
		},
		{
			name: "streaming enabled without live notifications",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
streaming:
  enabled: true
  live_notifications: false
`,
			want: StreamingConfig{
				Enabled:           true,
				LiveNotifications: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			require.NoError(t, err)

			assert.Equal(t, tt.want, got.Streaming)
		})
	}
}

func TestLoad_ExecutionConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want ExecutionConfig
	}{
		{
			name: "sandbox execution with warm pool",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
execution:
  backend: sandbox
  sandbox:
    runtime_class: gvisor
    env_stripping: true
    warm_pool:
      enabled: true
      size: 5
`,
			want: ExecutionConfig{
				Backend: "sandbox",
				Sandbox: SandboxConfig{
					RuntimeClass: "gvisor",
					EnvStripping: true,
					WarmPool: WarmPoolConfig{
						Enabled: true,
						Size:    5,
					},
				},
			},
		},
		{
			name: "job backend with no sandbox config",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
execution:
  backend: job
`,
			want: ExecutionConfig{
				Backend: "job",
			},
		},
		{
			name: "kata runtime class override",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
execution:
  backend: sandbox
  sandbox:
    runtime_class: kata
    env_stripping: false
`,
			want: ExecutionConfig{
				Backend: "sandbox",
				Sandbox: SandboxConfig{
					RuntimeClass: "kata",
				},
			},
		},
		{
			name: "defaults when execution not specified",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: ExecutionConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			require.NoError(t, err)

			assert.Equal(t, tt.want, got.Execution)
		})
	}
}

func TestLoad_GovernanceConfig(t *testing.T) {
	tests := []struct {
		name             string
		yaml             string
		wantGates        []string
		wantThreshold    float64
		wantStoreBackend string
	}{
		{
			name: "approval gates and cost threshold",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
  approval_gates:
    - pre_start
    - high_cost
    - pre_merge
  approval_cost_threshold_usd: 25.0
`,
			wantGates:     []string{"pre_start", "high_cost", "pre_merge"},
			wantThreshold: 25.0,
		},
		{
			name: "taskrun store with sqlite backend",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
taskrun_store:
  backend: sqlite
  sqlite:
    path: /var/lib/osmia/taskruns.db
`,
			wantStoreBackend: "sqlite",
		},
		{
			name: "memory store backend",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
taskrun_store:
  backend: memory
`,
			wantStoreBackend: "memory",
		},
		{
			name: "no governance config uses defaults",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantGates:        nil,
			wantThreshold:    0,
			wantStoreBackend: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			require.NoError(t, err)

			assert.Equal(t, tt.wantGates, got.GuardRails.ApprovalGates)
			assert.Equal(t, tt.wantThreshold, got.GuardRails.ApprovalCostThresholdUSD)
			assert.Equal(t, tt.wantStoreBackend, got.TaskRunStore.Backend)
		})
	}
}

func TestLoad_AgentTeamsConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want AgentTeamsConfig
	}{
		{
			name: "agent teams with mode and max teammates",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
  claude-code:
    agent_teams:
      enabled: true
      mode: in-process
      max_teammates: 4
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: AgentTeamsConfig{
				Enabled:      true,
				Mode:         "in-process",
				MaxTeammates: 4,
			},
		},
		{
			name: "agent teams without agents",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
  claude-code:
    agent_teams:
      enabled: true
      mode: in-process
      max_teammates: 3
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: AgentTeamsConfig{
				Enabled:      true,
				Mode:         "in-process",
				MaxTeammates: 3,
			},
		},
		{
			name: "agent teams disabled by default",
			yaml: `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			want: AgentTeamsConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			require.NoError(t, err)

			if got.Engines.ClaudeCode != nil {
				assert.Equal(t, tt.want, got.Engines.ClaudeCode.AgentTeams)
			} else {
				assert.Equal(t, tt.want, AgentTeamsConfig{})
			}
		})
	}
}

func TestLoad_LocalTicketingConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid local ticketing config",
			yaml: `
ticketing:
  backend: local
  config:
    store_path: /var/lib/osmia/local-ticketing.db
    seed_file: /var/lib/osmia/tasks.yaml
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
		},
		{
			name: "local ticketing requires store path",
			yaml: `
ticketing:
  backend: local
  config:
    seed_file: /var/lib/osmia/tasks.yaml
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantErr: "ticketing.config.store_path is required",
		},
		{
			name: "local ticketing rejects traversal in store path",
			yaml: `
ticketing:
  backend: local
  config:
    store_path: ../local-ticketing.db
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantErr: "ticketing.config.store_path contains directory traversal component",
		},
		{
			name: "local ticketing rejects traversal in seed file",
			yaml: `
ticketing:
  backend: local
  config:
    store_path: /var/lib/osmia/local-ticketing.db
    seed_file: ../tasks.yaml
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantErr: "ticketing.config.seed_file contains directory traversal component",
		},
		{
			name: "legacy task file is rejected",
			yaml: `
ticketing:
  config:
    task_file: /var/lib/osmia/tasks.yaml
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`,
			wantErr: "ticketing.config.task_file is no longer supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			got, err := Load(tmp)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "local", got.Ticketing.Backend)
			assert.Equal(t, "/var/lib/osmia/local-ticketing.db", got.Ticketing.Config["store_path"])
			assert.Equal(t, "/var/lib/osmia/tasks.yaml", got.Ticketing.Config["seed_file"])
		})
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/osmia-config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config file")
}

func TestLoad_GenericWebhookConfig(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
	err := os.WriteFile(tmp, []byte(`
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
webhook:
  enabled: true
  generic:
    auth_mode: hmac
    secret: shared-hmac-secret
    signature_header: X-Custom-Signature
    field_mapping:
      issue.id: id
      issue.title: title
      issue.body: description
`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(tmp)
	require.NoError(t, err)

	require.NotNil(t, cfg.Webhook.Generic)
	assert.Equal(t, "hmac", cfg.Webhook.Generic.AuthMode)
	assert.Equal(t, "shared-hmac-secret", cfg.Webhook.Generic.Secret)
	assert.Equal(t, "X-Custom-Signature", cfg.Webhook.Generic.SignatureHeader)
	assert.Equal(t, map[string]string{
		"issue.id":    "id",
		"issue.title": "title",
		"issue.body":  "description",
	}, cfg.Webhook.Generic.FieldMapping)
}

func TestLoad_GenericWebhookValidation(t *testing.T) {
	baseRequired := `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`

	tests := []struct {
		name       string
		genericCfg string
		wantErrMsg string
	}{
		{
			name: "valid hmac config",
			genericCfg: `
webhook:
  enabled: true
  generic:
    auth_mode: hmac
    secret: shared-hmac-secret
`,
		},
		{
			name: "valid bearer config",
			genericCfg: `
webhook:
  enabled: true
  generic:
    auth_mode: bearer
    secret: shared-bearer-token
`,
		},
		{
			name: "missing auth_mode",
			genericCfg: `
webhook:
  enabled: true
  generic:
    secret: shared-secret
`,
			wantErrMsg: "webhook.generic.auth_mode is required",
		},
		{
			name: "unsupported auth_mode",
			genericCfg: `
webhook:
  enabled: true
  generic:
    auth_mode: nope
    secret: shared-secret
`,
			wantErrMsg: `webhook.generic.auth_mode "nope" is not supported`,
		},
		{
			name: "missing secret",
			genericCfg: `
webhook:
  enabled: true
  generic:
    auth_mode: hmac
`,
			wantErrMsg: "webhook.generic.secret is required",
		},
		{
			name: "valid svix config",
			genericCfg: `
webhook:
  enabled: true
  generic:
    auth_mode: svix
    secret: whsec_abc123
`,
		},
		{
			name:       "no generic block — validation skipped",
			genericCfg: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(baseRequired+tt.genericCfg), 0o600)
			require.NoError(t, err)

			_, err = Load(tmp)
			if tt.wantErrMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrMsg)
		})
	}
}

func TestLoad_TaskRunStoreValidation(t *testing.T) {
	baseRequired := `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`

	tests := []struct {
		name           string
		taskRunStore   string
		wantErrMsg     string
		wantSQLitePath string
	}{
		{
			name:         "no taskrun_store block — defaults to memory",
			taskRunStore: "",
		},
		{
			name: "explicit memory backend",
			taskRunStore: `
taskrun_store:
  backend: memory
`,
		},
		{
			name: "sqlite backend with absolute path",
			taskRunStore: `
taskrun_store:
  backend: sqlite
  sqlite:
    path: /data/taskruns.db
`,
			wantSQLitePath: "/data/taskruns.db",
		},
		{
			name: "sqlite backend with no path — validation passes, default applied at startup",
			taskRunStore: `
taskrun_store:
  backend: sqlite
`,
		},
		{
			name: "sqlite backend with relative path is rejected",
			taskRunStore: `
taskrun_store:
  backend: sqlite
  sqlite:
    path: relative/taskruns.db
`,
			wantErrMsg: `taskrun_store.sqlite.path must be an absolute path, got "relative/taskruns.db"`,
		},
		{
			name: "postgres backend is not yet supported",
			taskRunStore: `
taskrun_store:
  backend: postgres
`,
			wantErrMsg: `taskrun_store.backend "postgres" is not yet supported`,
		},
		{
			name: "unrecognised backend is rejected",
			taskRunStore: `
taskrun_store:
  backend: mongodb
`,
			wantErrMsg: `taskrun_store.backend "mongodb" is not recognised`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(baseRequired+tt.taskRunStore), 0o600)
			require.NoError(t, err)

			cfg, err := Load(tmp)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			if tt.wantSQLitePath != "" {
				assert.Equal(t, tt.wantSQLitePath, cfg.TaskRunStore.SQLite.Path)
			}
		})
	}
}

func TestLoad_ExpandsEnvVars(t *testing.T) {
	tests := []struct {
		name      string
		envVars   map[string]string
		unsetVars []string // variables that must be unset before the test runs
		yaml      string
		wantToken string
	}{
		{
			name:    "expands ${VAR} form inside double-quoted scalar",
			envVars: map[string]string{"OSMIA_TEST_TOKEN": "from-braced-env"},
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: "${OSMIA_TEST_TOKEN}"
`,
			wantToken: "from-braced-env",
		},
		{
			name:    "expands $VAR form inside double-quoted scalar",
			envVars: map[string]string{"OSMIA_TEST_TOKEN": "from-bare-env"},
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: "$OSMIA_TEST_TOKEN"
`,
			wantToken: "from-bare-env",
		},
		{
			name:      "unset variable expands to empty string",
			unsetVars: []string{"OSMIA_TEST_UNSET"},
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: "prefix-${OSMIA_TEST_UNSET}-suffix"
`,
			wantToken: "prefix--suffix",
		},
		{
			name: "config without env references is unchanged",
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: plain-value
`,
			wantToken: "plain-value",
		},
		{
			name:    "double-quoting suppresses comment parsing in expanded value",
			envVars: map[string]string{"OSMIA_TEST_TOKEN": "abc # not-a-comment"},
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: "${OSMIA_TEST_TOKEN}"
`,
			wantToken: "abc # not-a-comment",
		},
		{
			name:    "double-quoting contains values with YAML-significant characters",
			envVars: map[string]string{"OSMIA_TEST_TOKEN": "{a: b}"},
			yaml: `
ticketing:
  backend: shortcut
  config:
    token_secret: "${OSMIA_TEST_TOKEN}"
`,
			wantToken: "{a: b}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			// Explicitly clear any vars the test asserts as unset, in case
			// they leaked in from the runner environment, and restore the
			// previous value (if any) when the subtest ends.
			for _, k := range tt.unsetVars {
				prev, had := os.LookupEnv(k)
				require.NoError(t, os.Unsetenv(k))
				if had {
					t.Cleanup(func() { _ = os.Setenv(k, prev) })
				}
			}

			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(tt.yaml), 0o600)
			require.NoError(t, err)

			cfg, err := Load(tmp)
			require.NoError(t, err)

			raw, ok := cfg.Ticketing.Config["token_secret"]
			require.True(t, ok, "token_secret should be present in ticketing.config")
			got, ok := raw.(string)
			require.True(t, ok, "token_secret should be a string, got %T", raw)
			assert.Equal(t, tt.wantToken, got)
		})
	}
}

func TestLoad_IncidentIOWebhookConfig(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
	err := os.WriteFile(tmp, []byte(`
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
webhook:
  enabled: true
  incident_io:
    auth_mode: svix
    secret: whsec_abc123
incident_triage:
  engine: claude-code
  append_system_prompt: |
    You are a triage agent. Invoke /incident-classifier.
  slack_channel_id: C_INCIDENT
  slack_token_secret: incident-bot-token
  incident_io_api_key_secret: incident-io-api-key
`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(tmp)
	require.NoError(t, err)

	require.NotNil(t, cfg.Webhook.IncidentIO)
	assert.Equal(t, "svix", cfg.Webhook.IncidentIO.AuthMode)
	assert.Equal(t, "whsec_abc123", cfg.Webhook.IncidentIO.Secret)

	assert.Equal(t, "claude-code", cfg.IncidentTriage.Engine)
	assert.Contains(t, cfg.IncidentTriage.AppendSystemPrompt, "incident-classifier")
	assert.Equal(t, "C_INCIDENT", cfg.IncidentTriage.SlackChannelID)
	assert.Equal(t, "incident-bot-token", cfg.IncidentTriage.SlackTokenSecret)
	assert.Equal(t, "incident-io-api-key", cfg.IncidentTriage.IncidentIOAPIKeySecret)
}

func TestLoad_IncidentIOWebhookValidation(t *testing.T) {
	baseRequired := `
ticketing:
  backend: github
secrets:
  backend: env
engines:
  default: claude-code
guardrails:
  max_cost_per_job: 5.0
  max_concurrent_jobs: 10
  max_job_duration_minutes: 60
`

	tests := []struct {
		name       string
		incidentIO string
		wantErrMsg string
	}{
		{
			name: "valid svix config",
			incidentIO: `
webhook:
  enabled: true
  incident_io:
    auth_mode: svix
    secret: whsec_abc123
`,
		},
		{
			name: "missing auth_mode",
			incidentIO: `
webhook:
  enabled: true
  incident_io:
    secret: whsec_abc123
`,
			wantErrMsg: "webhook.incident_io.auth_mode is required",
		},
		{
			name: "unsupported auth_mode",
			incidentIO: `
webhook:
  enabled: true
  incident_io:
    auth_mode: hmac
    secret: whsec_abc123
`,
			wantErrMsg: `webhook.incident_io.auth_mode "hmac" is not supported`,
		},
		{
			name: "missing secret",
			incidentIO: `
webhook:
  enabled: true
  incident_io:
    auth_mode: svix
`,
			wantErrMsg: "webhook.incident_io.secret is required",
		},
		{
			name:       "no incident_io block — validation skipped",
			incidentIO: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "osmia-config.yaml")
			err := os.WriteFile(tmp, []byte(baseRequired+tt.incidentIO), 0o600)
			require.NoError(t, err)

			_, err = Load(tmp)
			if tt.wantErrMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrMsg)
		})
	}
}

func TestEnginesConfig_AuthFor(t *testing.T) {
	claudeAuth := AuthConfig{Method: "api_key", APIKeySecret: "claude-secret"}
	codexAuth := AuthConfig{Method: "api_key", APIKeySecret: "codex-secret"}
	aiderAuth := AuthConfig{Method: "api_key", APIKeySecret: "aider-secret"}
	opencodeAuth := AuthConfig{Method: "api_key", APIKeySecret: "opencode-secret"}
	clineAuth := AuthConfig{Method: "api_key", APIKeySecret: "cline-secret"}

	engines := EnginesConfig{
		ClaudeCode: &ClaudeCodeEngineConfig{Auth: claudeAuth},
		Codex:      &CodexEngineConfig{Auth: codexAuth},
		Aider:      &AiderEngineConfig{Auth: aiderAuth},
		OpenCode:   &OpenCodeEngineConfig{Auth: opencodeAuth},
		Cline:      &ClineEngineConfig{Auth: clineAuth},
	}

	tests := []struct {
		name       string
		engines    *EnginesConfig
		engineName string
		wantAuth   AuthConfig
		wantOK     bool
	}{
		{name: "claude-code", engines: &engines, engineName: "claude-code", wantAuth: claudeAuth, wantOK: true},
		{name: "codex", engines: &engines, engineName: "codex", wantAuth: codexAuth, wantOK: true},
		{name: "aider", engines: &engines, engineName: "aider", wantAuth: aiderAuth, wantOK: true},
		{name: "opencode", engines: &engines, engineName: "opencode", wantAuth: opencodeAuth, wantOK: true},
		{name: "cline", engines: &engines, engineName: "cline", wantAuth: clineAuth, wantOK: true},
		{name: "unrecognised engine name", engines: &engines, engineName: "unknown", wantOK: false},
		{name: "engine block not configured", engines: &EnginesConfig{}, engineName: "claude-code", wantOK: false},
		{name: "nil EnginesConfig", engines: nil, engineName: "claude-code", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAuth, gotOK := tt.engines.AuthFor(tt.engineName)
			assert.Equal(t, tt.wantOK, gotOK)
			assert.Equal(t, tt.wantAuth, gotAuth)
		})
	}
}
