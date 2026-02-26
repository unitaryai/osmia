// Package config loads and validates RoboDev controller configuration
// from a YAML file (robodev-config.yaml).
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for the RoboDev controller,
// loaded from robodev-config.yaml.
type Config struct {
	Ticketing      TicketingConfig      `yaml:"ticketing"`
	Notifications  NotificationsConfig  `yaml:"notifications"`
	Secrets        SecretsConfig        `yaml:"secrets"`
	Engines        EnginesConfig        `yaml:"engines"`
	GuardRails     GuardRailsConfig     `yaml:"guardrails"`
	PluginHealth   PluginHealthConfig   `yaml:"plugin_health"`
	QualityGate    QualityGateConfig    `yaml:"quality_gate"`
	Tenancy        TenancyConfig        `yaml:"tenancy"`
	Review         ReviewConfig         `yaml:"review"`
	SCM            SCMConfig            `yaml:"scm"`
	ProgressWatchdog WatchdogConfig     `yaml:"progress_watchdog"`
}

// TicketingConfig configures the ticketing backend.
type TicketingConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// NotificationsConfig configures notification channels.
type NotificationsConfig struct {
	Channels []ChannelConfig `yaml:"channels"`
}

// ChannelConfig configures a single notification channel.
type ChannelConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// SecretsConfig configures the secrets backend.
type SecretsConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// EnginesConfig configures available execution engines.
type EnginesConfig struct {
	Default    string                  `yaml:"default"`
	ClaudeCode *ClaudeCodeEngineConfig `yaml:"claude-code,omitempty"`
	Codex      *CodexEngineConfig      `yaml:"codex,omitempty"`
	Aider      *AiderEngineConfig      `yaml:"aider,omitempty"`
}

// ClaudeCodeEngineConfig holds Claude Code-specific engine settings.
type ClaudeCodeEngineConfig struct {
	Image      string           `yaml:"image,omitempty"`
	Auth       AuthConfig       `yaml:"auth"`
	AgentTeams AgentTeamsConfig `yaml:"agent_teams"`
}

// CodexEngineConfig holds OpenAI Codex-specific engine settings.
type CodexEngineConfig struct {
	Image string     `yaml:"image,omitempty"`
	Auth  AuthConfig `yaml:"auth"`
}

// AiderEngineConfig holds Aider-specific engine settings.
type AiderEngineConfig struct {
	Image string     `yaml:"image,omitempty"`
	Auth  AuthConfig `yaml:"auth"`
}

// AuthConfig configures authentication for an execution engine.
type AuthConfig struct {
	Method           string `yaml:"method"`             // "api_key", "setup_token", "bedrock", "vertex", "credentials_file"
	APIKeySecret     string `yaml:"api_key_secret"`     // K8s Secret name for API key
	BedrockRegion    string `yaml:"bedrock_region"`     // AWS region for Bedrock
	CredentialsSecret string `yaml:"credentials_secret"` // K8s Secret name for credentials file
}

// AgentTeamsConfig configures experimental agent teams for Claude Code.
type AgentTeamsConfig struct {
	Enabled      bool `yaml:"enabled"`
	Mode         string `yaml:"mode"`          // "in-process"
	MaxTeammates int    `yaml:"max_teammates"`
}

// GuardRailsConfig configures controller-level safety boundaries.
type GuardRailsConfig struct {
	MaxCostPerJob                float64  `yaml:"max_cost_per_job"`
	MaxConcurrentJobs            int      `yaml:"max_concurrent_jobs"`
	MaxJobDurationMinutes        int      `yaml:"max_job_duration_minutes"`
	AllowedRepos                 []string `yaml:"allowed_repos"`
	BlockedFilePatterns          []string `yaml:"blocked_file_patterns"`
	RequireHumanApprovalBeforeMR bool     `yaml:"require_human_approval_before_mr"`
	AllowedTaskTypes             []string `yaml:"allowed_task_types"`
}

// PluginHealthConfig configures plugin health monitoring.
type PluginHealthConfig struct {
	MaxPluginRestarts int      `yaml:"max_plugin_restarts"`
	RestartBackoff    []int    `yaml:"restart_backoff"`
	CriticalPlugins   []string `yaml:"critical_plugins"`
}

// QualityGateConfig configures the optional quality gate.
type QualityGateConfig struct {
	Enabled           bool    `yaml:"enabled"`
	Mode              string  `yaml:"mode"`               // "post-completion" or "security-only"
	Engine            string  `yaml:"engine"`              // Engine to use for reviews
	MaxCostPerReview  float64 `yaml:"max_cost_per_review"`
	SecurityChecks    SecurityChecksConfig `yaml:"security_checks"`
	OnFailure         string  `yaml:"on_failure"`          // "retry_with_feedback", "block_mr", "notify_human"
}

// SecurityChecksConfig configures quality gate security checks.
type SecurityChecksConfig struct {
	ScanForSecrets            bool `yaml:"scan_for_secrets"`
	CheckOWASPPatterns        bool `yaml:"check_owasp_patterns"`
	VerifyGuardrailCompliance bool `yaml:"verify_guardrail_compliance"`
	CheckDependencyCVEs       bool `yaml:"check_dependency_cves"`
}

// TenancyConfig configures multi-tenancy support.
type TenancyConfig struct {
	Mode    string         `yaml:"mode"` // "shared" or "namespace-per-tenant"
	Tenants []TenantConfig `yaml:"tenants,omitempty"`
}

// TenantConfig configures a single tenant in namespace-per-tenant mode.
type TenantConfig struct {
	Name      string          `yaml:"name"`
	Namespace string          `yaml:"namespace"`
	Ticketing TicketingConfig `yaml:"ticketing"`
	Secrets   SecretsConfig   `yaml:"secrets"`
}

// ReviewConfig configures the review backend.
type ReviewConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// SCMConfig configures the source code management backend.
type SCMConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// WatchdogConfig configures the progress watchdog.
type WatchdogConfig struct {
	Enabled                    bool    `yaml:"enabled"`
	CheckIntervalSeconds       int     `yaml:"check_interval_seconds"`
	MinConsecutiveTicks        int     `yaml:"min_consecutive_ticks"`
	ResearchGracePeriodMinutes int     `yaml:"research_grace_period_minutes"`
	LoopDetectionThreshold     int     `yaml:"loop_detection_threshold"`
	ThrashingTokenThreshold    int     `yaml:"thrashing_token_threshold"`
	StallIdleSeconds           int     `yaml:"stall_idle_seconds"`
	CostVelocityMaxPer10Min   float64 `yaml:"cost_velocity_max_per_10_min"`
	UnansweredHumanTimeoutMin  int     `yaml:"unanswered_human_timeout_minutes"`
}

// Load reads and parses a RoboDev configuration file from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return cfg, nil
}
