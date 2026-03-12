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
