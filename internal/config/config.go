// Package config loads and validates Osmia controller configuration
// from a YAML file (osmia-config.yaml).
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for the Osmia controller,
// loaded from osmia-config.yaml.
type Config struct {
	Ticketing            TicketingConfig            `yaml:"ticketing"`
	Notifications        NotificationsConfig        `yaml:"notifications"`
	Secrets              SecretsConfig              `yaml:"secrets"`
	Engines              EnginesConfig              `yaml:"engines"`
	Execution            ExecutionConfig            `yaml:"execution"`
	GuardRails           GuardRailsConfig           `yaml:"guardrails"`
	PluginHealth         PluginHealthConfig         `yaml:"plugin_health"`
	QualityGate          QualityGateConfig          `yaml:"quality_gate"`
	Tenancy              TenancyConfig              `yaml:"tenancy"`
	Approval             ApprovalConfig             `yaml:"approval"`
	Review               ReviewConfig               `yaml:"review"`
	CodeReview           CodeReviewConfig           `yaml:"code_review"`
	SCM                  SCMConfig                  `yaml:"scm"`
	ProgressWatchdog     WatchdogConfig             `yaml:"progress_watchdog"`
	Webhook              WebhookConfig              `yaml:"webhook"`
	SecretResolver       SecretResolverConfig       `yaml:"secret_resolver"`
	Streaming            StreamingConfig            `yaml:"streaming"`
	TaskRunStore         TaskRunStoreConfig         `yaml:"taskrun_store"`
	Routing              RoutingConfig              `yaml:"routing"`
	Diagnosis            DiagnosisConfig            `yaml:"diagnosis"`
	PRM                  PRMConfig                  `yaml:"prm"`
	Memory               MemoryConfig               `yaml:"memory"`
	Estimator            EstimatorConfig            `yaml:"estimator"`
	CompetitiveExecution CompetitiveExecutionConfig `yaml:"competitive_execution"`
	Audit                AuditConfig                `yaml:"audit"`
	ReviewResponse       ReviewResponseConfig       `yaml:"review_response"`
}

// ReviewResponseConfig configures the PR/MR review comment response subsystem.
// When enabled, Osmia monitors open pull/merge requests it created and
// spawns follow-up jobs to address actionable review feedback.
type ReviewResponseConfig struct {
	// Enabled activates the review response subsystem.
	Enabled bool `yaml:"enabled"`
	// PollIntervalMinutes is how often to check for new comments. Default: 5.
	PollIntervalMinutes int `yaml:"poll_interval_minutes"`
	// MinSeverity sets the minimum comment severity that triggers a follow-up.
	// Must be one of "info", "warning", or "error". Default: "warning".
	MinSeverity string `yaml:"min_severity"`
	// MaxFollowUpJobs is the maximum number of follow-up jobs per PR. Default: 3.
	MaxFollowUpJobs int `yaml:"max_follow_up_jobs"`
	// ReplyToComments causes Osmia to reply to actionable comments
	// acknowledging that it is addressing them. Default: true.
	ReplyToComments bool `yaml:"reply_to_comments"`
	// ResolveThreads causes Osmia to resolve the discussion thread once
	// the follow-up job completes. Not supported by GitHub REST. Default: false.
	ResolveThreads bool `yaml:"resolve_threads"`
	// LLMClassifier enables LLM-backed comment classification with rule-based
	// fallback. Requires an LLM client to be configured. Default: false.
	LLMClassifier bool `yaml:"llm_classifier"`
	// SettlingMinutes is the minimum time to wait after a PR is registered
	// before polling it for comments. This gives review bots (e.g. CodeRabbit)
	// time to finish posting all their comments before Osmia acts. Default: 0
	// (poll immediately on next interval).
	SettlingMinutes int `yaml:"settling_minutes"`
	// IgnoreSummaryAuthors is a list of regex patterns matching comment
	// author usernames whose non-inline comments (no file position) are
	// ignored. Inline diff comments from these authors are still processed
	// so that actionable review feedback is not lost. Merged with built-in
	// defaults (e.g. "osmia", "dependabot"). Case-insensitive.
	IgnoreSummaryAuthors []string `yaml:"ignore_summary_authors,omitempty"`
}

// CompetitiveExecutionConfig configures competitive execution with tournament selection.
type CompetitiveExecutionConfig struct {
	Enabled                   bool    `yaml:"enabled"`
	DefaultCandidates         int     `yaml:"default_candidates"`
	JudgeEngine               string  `yaml:"judge_engine"`
	EarlyTerminationThreshold float64 `yaml:"early_termination_threshold"`
	MaxConcurrentTournaments  int     `yaml:"max_concurrent_tournaments"`
}

// PRMConfig configures the Process Reward Model for real-time agent coaching.
type PRMConfig struct {
	Enabled                bool    `yaml:"enabled"`
	EvaluationInterval     int     `yaml:"evaluation_interval"`
	WindowSize             int     `yaml:"window_size"`
	ScoreThresholdNudge    int     `yaml:"score_threshold_nudge"`
	ScoreThresholdEscalate int     `yaml:"score_threshold_escalate"`
	HintFilePath           string  `yaml:"hint_file_path"`
	MaxTrajectoryLength    int     `yaml:"max_trajectory_length"`
	MaxBudgetUSD           float64 `yaml:"max_budget_usd"`
	// UseLLMScoring enables LLM-backed step scoring with rule-based fallback.
	UseLLMScoring bool `yaml:"use_llm_scoring"`
}

// ExecutionConfig configures how agent workloads are executed.
type ExecutionConfig struct {
	Backend string        `yaml:"backend"` // "job", "sandbox", or "local"
	Sandbox SandboxConfig `yaml:"sandbox,omitempty"`
}

// SandboxConfig holds settings for gVisor-based sandboxed execution.
type SandboxConfig struct {
	RuntimeClass string         `yaml:"runtime_class"` // e.g. "gvisor", "kata"
	WarmPool     WarmPoolConfig `yaml:"warm_pool,omitempty"`
	EnvStripping bool           `yaml:"env_stripping"`
}

// WarmPoolConfig configures pre-warmed sandbox pools for faster startup.
type WarmPoolConfig struct {
	Enabled bool `yaml:"enabled"`
	Size    int  `yaml:"size"`
}

// StreamingConfig configures real-time streaming of agent output events.
type StreamingConfig struct {
	Enabled           bool `yaml:"enabled"`
	LiveNotifications bool `yaml:"live_notifications"`
}

// WebhookConfig configures the optional webhook receiver server.
type WebhookConfig struct {
	Enabled  bool                  `yaml:"enabled"`
	Port     int                   `yaml:"port,omitempty"` // defaults to 8081
	GitHub   *WebhookSourceConfig  `yaml:"github,omitempty"`
	GitLab   *WebhookSourceConfig  `yaml:"gitlab,omitempty"`
	Slack    *WebhookSourceConfig  `yaml:"slack,omitempty"`
	Shortcut *WebhookSourceConfig  `yaml:"shortcut,omitempty"`
	Generic  *GenericWebhookConfig `yaml:"generic,omitempty"`
}

// WebhookSourceConfig holds the shared secret for a webhook source.
type WebhookSourceConfig struct {
	Secret        string   `yaml:"secret"`                   // HMAC secret or validation token
	TriggerLabels []string `yaml:"trigger_labels,omitempty"` // only forward issues carrying at least one of these labels
}

// GenericWebhookConfig holds settings for the generic webhook handler.
type GenericWebhookConfig struct {
	Secret    string            `yaml:"secret,omitempty"`     // HMAC secret
	AuthToken string            `yaml:"auth_token,omitempty"` // bearer token
	FieldMap  map[string]string `yaml:"field_map,omitempty"`  // JSON field mapping
}

// SecretResolverConfig configures the task-scoped secret resolver.
type SecretResolverConfig struct {
	Backends []BackendRef               `yaml:"backends,omitempty"`
	Aliases  map[string]AliasConfig     `yaml:"aliases,omitempty"`
	Policy   SecretResolverPolicyConfig `yaml:"policy"`
}

// BackendRef references a secret backend by scheme and type.
type BackendRef struct {
	Scheme  string         `yaml:"scheme"`  // e.g. "vault", "k8s", "aws-sm"
	Backend string         `yaml:"backend"` // backend type name
	Config  map[string]any `yaml:"config,omitempty"`
}

// AliasConfig maps a friendly alias name to a concrete secret URI.
type AliasConfig struct {
	URI      string `yaml:"uri"`
	TenantID string `yaml:"tenant_id,omitempty"`
}

// SecretResolverPolicyConfig controls which secrets can be requested.
type SecretResolverPolicyConfig struct {
	AllowedEnvPatterns []string `yaml:"allowed_env_patterns,omitempty"`
	BlockedEnvPatterns []string `yaml:"blocked_env_patterns,omitempty"`
	AllowRawRefs       bool     `yaml:"allow_raw_refs"`
	AllowedSchemes     []string `yaml:"allowed_schemes,omitempty"`
}

// VaultSecretsConfig holds HashiCorp Vault-specific configuration.
type VaultSecretsConfig struct {
	Address     string `yaml:"address"`
	AuthMethod  string `yaml:"auth_method"` // "kubernetes"
	Role        string `yaml:"role"`
	SecretsPath string `yaml:"secrets_path"` // e.g. "secret"
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
	Default         string                  `yaml:"default"`
	FallbackEngines []string                `yaml:"fallback_engines"`
	ClaudeCode      *ClaudeCodeEngineConfig `yaml:"claude-code,omitempty"`
	Codex           *CodexEngineConfig      `yaml:"codex,omitempty"`
	Aider           *AiderEngineConfig      `yaml:"aider,omitempty"`
	OpenCode        *OpenCodeEngineConfig   `yaml:"opencode,omitempty"`
	Cline           *ClineEngineConfig      `yaml:"cline,omitempty"`
}

// ImageFor returns the configured container image for the named engine, or an
// empty string if no override is set (the engine will use its built-in default).
func (e *EnginesConfig) ImageFor(engineName string) string {
	if e == nil {
		return ""
	}
	switch engineName {
	case "claude-code":
		if e.ClaudeCode != nil {
			return e.ClaudeCode.Image
		}
	case "codex":
		if e.Codex != nil {
			return e.Codex.Image
		}
	case "aider":
		if e.Aider != nil {
			return e.Aider.Image
		}
	case "opencode":
		if e.OpenCode != nil {
			return e.OpenCode.Image
		}
	case "cline":
		if e.Cline != nil {
			return e.Cline.Image
		}
	}
	return ""
}

// OpenCodeEngineConfig holds OpenCode-specific engine settings.
type OpenCodeEngineConfig struct {
	Image    string     `yaml:"image,omitempty"`
	Auth     AuthConfig `yaml:"auth"`
	Provider string     `yaml:"provider,omitempty"` // "anthropic", "openai", "google"
}

// ClineEngineConfig holds Cline-specific engine settings.
type ClineEngineConfig struct {
	Image      string     `yaml:"image,omitempty"`
	Auth       AuthConfig `yaml:"auth"`
	Provider   string     `yaml:"provider,omitempty"` // "anthropic", "openai", "google", "bedrock"
	MCPEnabled bool       `yaml:"mcp_enabled,omitempty"`
}

// SkillConfig defines a custom skill to make available to the Claude Code agent.
// Claude Code agents can invoke skills via /skill-name in their prompts.
// Exactly one of Inline, Path, or ConfigMap must be set.
type SkillConfig struct {
	// Name is the skill identifier used as the filename stem (e.g. "create-changelog").
	// Use only lowercase letters, digits, and hyphens.
	Name string `yaml:"name"`
	// Path is the path to a Markdown skill file on the container image,
	// e.g. "/opt/osmia/skills/create-changelog.md". Use for bundled skills.
	Path string `yaml:"path,omitempty"`
	// Inline contains the skill Markdown content directly in the config.
	// Use for custom, operator-defined skills that do not require a pre-built image.
	Inline string `yaml:"inline,omitempty"`
	// ConfigMap is the name of a Kubernetes ConfigMap containing the skill Markdown.
	// The ConfigMap is mounted as a volume and the content is copied to
	// ~/.claude/skills/ at container startup. Use for large or independently
	// managed skills that should not be inlined in the controller config.
	ConfigMap string `yaml:"configmap,omitempty"`
	// Key is the key within the ConfigMap that holds the skill content.
	// Defaults to "<name>.md" when omitted.
	Key string `yaml:"key,omitempty"`
}

// SubAgentConfig defines a Claude Code sub-agent that can be delegated to
// during task execution. Sub-agents are passed via the --agents CLI flag
// (for inline definitions) or written to ~/.claude/agents/ (for ConfigMap-backed).
type SubAgentConfig struct {
	// Name is the sub-agent identifier (used as the key in the --agents JSON map).
	Name string `yaml:"name"`
	// Description is a short summary of what this sub-agent does.
	Description string `yaml:"description"`
	// Prompt is the inline system prompt for the sub-agent.
	Prompt string `yaml:"prompt,omitempty"`
	// Model selects the model: "sonnet", "opus", "haiku", or "inherit".
	Model string `yaml:"model,omitempty"`
	// Tools is an allowlist of tools the sub-agent may use.
	Tools []string `yaml:"tools,omitempty"`
	// DisallowedTools is a denylist of tools the sub-agent must not use.
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`
	// PermissionMode controls the sub-agent's permission behaviour.
	// One of "default", "acceptEdits", "dontAsk", "bypassPermissions", "plan".
	PermissionMode string `yaml:"permission_mode,omitempty"`
	// MaxTurns limits the number of agentic turns for this sub-agent.
	MaxTurns int `yaml:"max_turns,omitempty"`
	// Skills lists skill names to preload for this sub-agent.
	Skills []string `yaml:"skills,omitempty"`
	// Background runs the sub-agent as a background process.
	Background bool `yaml:"background,omitempty"`
	// ConfigMap loads the sub-agent prompt from a Kubernetes ConfigMap
	// instead of inline. The file is mounted and written to ~/.claude/agents/.
	ConfigMap string `yaml:"configmap,omitempty"`
	// Key is the key within the ConfigMap (defaults to "<name>.md").
	Key string `yaml:"key,omitempty"`
}

// SessionPersistenceConfig configures session-state persistence between retries.
// When enabled, Claude Code's ~/.claude directory (and optionally the workspace)
// is stored in a durable backend so that retry pods can resume the exact
// conversation context via --resume instead of starting a fresh session.
type SessionPersistenceConfig struct {
	// Enabled activates session persistence. Disabled by default; git-based
	// continuation remains the fallback when this is false.
	Enabled bool `yaml:"enabled"`
	// Backend selects the storage backend: "shared-pvc", "per-taskrun-pvc", or "s3".
	Backend string `yaml:"backend"`
	// PVCName is the name of the shared PVC to use when Backend is "shared-pvc".
	PVCName string `yaml:"pvc_name,omitempty"`
	// StorageClass is the storage class for dynamically provisioned PVCs
	// when Backend is "per-taskrun-pvc".
	StorageClass string `yaml:"storage_class,omitempty"`
	// StorageSize is the PVC size when Backend is "per-taskrun-pvc".
	// Defaults to "1Gi" when unset.
	StorageSize string `yaml:"storage_size,omitempty"`
	// S3Bucket is the S3 bucket name when Backend is "s3".
	S3Bucket string `yaml:"s3_bucket,omitempty"`
	// S3Prefix is the key prefix within the bucket. Defaults to "osmia-sessions/".
	S3Prefix string `yaml:"s3_prefix,omitempty"`
	// TTLMinutes is how long session data is retained after a TaskRun reaches
	// a terminal state. Defaults to 1440 (24 hours).
	TTLMinutes int `yaml:"ttl_minutes,omitempty"`
}

// ClaudeCodeEngineConfig holds Claude Code-specific engine settings.
type ClaudeCodeEngineConfig struct {
	Image string     `yaml:"image,omitempty"`
	Auth  AuthConfig `yaml:"auth"`
	// AgentTeams enables Claude Code's experimental agent teams feature,
	// which spawns multiple independent Claude Code instances that collaborate
	// via shared task lists and inter-agent messaging.
	AgentTeams    AgentTeamsConfig `yaml:"agent_teams"`
	FallbackModel string           `yaml:"fallback_model,omitempty"`
	// MaxTurns overrides the maximum number of agentic turns (--max-turns).
	// Defaults to 50 when unset. Increase this for tasks that require more
	// steps, such as large refactors or multi-file changes.
	MaxTurns           int      `yaml:"max_turns,omitempty"`
	ToolWhitelist      []string `yaml:"tool_whitelist,omitempty"`
	ToolBlacklist      []string `yaml:"tool_blacklist,omitempty"`
	JSONSchema         string   `yaml:"json_schema,omitempty"`
	AppendSystemPrompt string   `yaml:"append_system_prompt,omitempty"`
	// SessionPersistence configures opt-in session-state persistence between
	// retries. When disabled (the default), the git-based continuation
	// strategy is used instead.
	SessionPersistence SessionPersistenceConfig `yaml:"session_persistence,omitempty"`
	// Skills lists custom skill files to make available to the agent.
	// Each skill is written to ~/.claude/skills/<name>.md before the agent starts.
	Skills []SkillConfig `yaml:"skills,omitempty"`
	// SubAgents defines sub-agents that the main Claude Code agent can delegate
	// to during task execution. Sub-agents are lightweight helpers within a
	// single session — distinct from agent teams, which spawn independent instances.
	SubAgents []SubAgentConfig `yaml:"sub_agents,omitempty"`
	// ContinuationPrompt enables user-prompted continuation when the agent
	// exhausts --max-turns. Requires session persistence and an approval backend.
	// When true, the controller sends an approval request (e.g. Slack buttons)
	// asking whether to continue or stop, rather than auto-retrying or failing.
	ContinuationPrompt bool `yaml:"continuation_prompt,omitempty"`
	// MaxContinuations limits how many user-approved continuations are allowed
	// per TaskRun. Defaults to 3.
	MaxContinuations int `yaml:"max_continuations,omitempty"`
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
	Method            string `yaml:"method"`             // "api_key", "setup_token", "bedrock", "vertex", "credentials_file"
	APIKeySecret      string `yaml:"api_key_secret"`     // K8s Secret name for API key
	APIKeyKey         string `yaml:"api_key_key"`        // Key within the secret (default: tries ANTHROPIC_API_KEY, then api_key)
	BedrockRegion     string `yaml:"bedrock_region"`     // AWS region for Bedrock
	CredentialsSecret string `yaml:"credentials_secret"` // K8s Secret name for credentials file
}

// AgentTeamsConfig configures experimental agent teams for Claude Code.
// Agent teams spawn multiple independent Claude Code instances that collaborate
// via shared task lists and inter-agent messaging. The team lead dynamically
// creates teammates based on the task — agents are not pre-defined.
type AgentTeamsConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Mode          string `yaml:"mode"` // "in-process" (default) or "tmux"
	MaxTeammates  int    `yaml:"max_teammates"`
	TeammateModel string `yaml:"teammate_model"` // e.g. "sonnet", "claude-sonnet-4-6"
}

// GuardRailsConfig configures controller-level safety boundaries.
type GuardRailsConfig struct {
	MaxCostPerJob            float64                      `yaml:"max_cost_per_job"`
	MaxConcurrentJobs        int                          `yaml:"max_concurrent_jobs"`
	MaxJobDurationMinutes    int                          `yaml:"max_job_duration_minutes"`
	AllowedRepos             []string                     `yaml:"allowed_repos"`
	BlockedFilePatterns      []string                     `yaml:"blocked_file_patterns"`
	AllowedTaskTypes         []string                     `yaml:"allowed_task_types"`
	TaskProfiles             map[string]TaskProfileConfig `yaml:"task_profiles,omitempty"`
	ApprovalGates            []string                     `yaml:"approval_gates,omitempty"`
	ApprovalCostThresholdUSD float64                      `yaml:"approval_cost_threshold_usd,omitempty"`
}

// TaskRunStoreConfig configures the persistent TaskRun store backend.
type TaskRunStoreConfig struct {
	Backend string            `yaml:"backend"` // "memory", "sqlite", "postgres"
	SQLite  SQLiteStoreConfig `yaml:"sqlite,omitempty"`
}

// SQLiteStoreConfig holds SQLite-specific store settings.
type SQLiteStoreConfig struct {
	Path string `yaml:"path"`
}

// TaskProfileConfig configures per-task-type behaviour such as workflow mode
// and tool restrictions.
type TaskProfileConfig struct {
	Workflow      string   `yaml:"workflow,omitempty"`       // "tdd", "review-first", or "" for default
	ToolWhitelist []string `yaml:"tool_whitelist,omitempty"` // allowed tool commands
	ToolBlacklist []string `yaml:"tool_blacklist,omitempty"` // blocked tool commands
	// MCPServers lists additional MCP server names to enable for tasks matching
	// this profile, merged on top of the global engine MCP configuration.
	// Full runtime wiring requires setup-claude.sh to merge per-profile servers
	// into the workspace MCP config at job startup.
	MCPServers []string `yaml:"mcp_servers,omitempty"`
}

// PluginHealthConfig configures plugin health monitoring.
type PluginHealthConfig struct {
	MaxPluginRestarts int      `yaml:"max_plugin_restarts"`
	RestartBackoff    []int    `yaml:"restart_backoff"`
	CriticalPlugins   []string `yaml:"critical_plugins"`
}

// QualityGateConfig configures the optional quality gate.
type QualityGateConfig struct {
	Enabled          bool                 `yaml:"enabled"`
	Mode             string               `yaml:"mode"`   // "post-completion" or "security-only"
	Engine           string               `yaml:"engine"` // Engine to use for reviews
	MaxCostPerReview float64              `yaml:"max_cost_per_review"`
	SecurityChecks   SecurityChecksConfig `yaml:"security_checks"`
	OnFailure        string               `yaml:"on_failure"` // "retry_with_feedback", "block_mr", "notify_human"
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

// ApprovalConfig configures the human approval backend used for
// interactive approval gates (pre_start, pre_merge, etc).
type ApprovalConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// ReviewConfig configures the review backend.
type ReviewConfig struct {
	Backend string         `yaml:"backend"`
	Config  map[string]any `yaml:"config"`
}

// SCMBackendEntry configures a single backend in a multi-backend SCM router.
// The Match field is matched against the host of the repository URL.
type SCMBackendEntry struct {
	Backend string         `yaml:"backend"` // "github" | "gitlab"
	Match   string         `yaml:"match"`   // host or glob pattern, e.g. "github.com" or "*.internal.example.com"
	Config  map[string]any `yaml:"config"`
}

// SCMConfig configures the source code management backend.
// Use the Backends array for multi-backend routing; the single Backend/Config
// fields are kept for backwards compatibility and take effect when Backends is
// empty.
// DefaultSCMBranchPrefix is the default branch prefix when none is configured.
const DefaultSCMBranchPrefix = "osmia/"

type SCMConfig struct {
	Backend  string            `yaml:"backend"`
	Config   map[string]any    `yaml:"config"`
	Backends []SCMBackendEntry `yaml:"backends,omitempty"`
	// BranchPrefix is prepended to the ticket ID to form the git branch
	// name (e.g. "sc-" produces "sc-28671", "osmia/" produces "osmia/28671").
	// Also used as the ticket reference prefix in MR titles (e.g. "[sc-28671]").
	// Default: "osmia/".
	BranchPrefix string `yaml:"branch_prefix"`
}

// ShortcutWorkflow configures a single trigger→in-progress workflow mapping for
// the Shortcut ticketing backend. Multiple entries allow Osmia to pick up
// stories from different workflow states, each transitioning to its own
// in-progress state.
type ShortcutWorkflow struct {
	TriggerState    string `yaml:"trigger_state"`     // e.g. "Ready for Development"
	InProgressState string `yaml:"in_progress_state"` // e.g. "In Development"
}

// CodeReviewConfig configures the optional automated code review gate. When
// enabled, the controller requests a review from the configured backend after a
// job completes and optionally waits for comments before marking the task
// complete. Set enabled: false (the default) to skip the review gate entirely.
type CodeReviewConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Backend         string `yaml:"backend"`           // "coderabbit" | "custom" | "none"
	WaitForComments bool   `yaml:"wait_for_comments"` // wait for review before marking complete
	TimeoutMinutes  int    `yaml:"timeout_minutes"`   // give up waiting after this many minutes
	TokenSecret     string `yaml:"token_secret"`      // K8s Secret name for the review token
}

// WatchdogConfig configures the progress watchdog.
type WatchdogConfig struct {
	Enabled                    bool                      `yaml:"enabled"`
	CheckIntervalSeconds       int                       `yaml:"check_interval_seconds"`
	MinConsecutiveTicks        int                       `yaml:"min_consecutive_ticks"`
	ResearchGracePeriodMinutes int                       `yaml:"research_grace_period_minutes"`
	LoopDetectionThreshold     int                       `yaml:"loop_detection_threshold"`
	ThrashingTokenThreshold    int                       `yaml:"thrashing_token_threshold"`
	StallIdleSeconds           int                       `yaml:"stall_idle_seconds"`
	CostVelocityMaxPer10Min    float64                   `yaml:"cost_velocity_max_per_10_min"`
	UnansweredHumanTimeoutMin  int                       `yaml:"unanswered_human_timeout_minutes"`
	AdaptiveCalibration        AdaptiveCalibrationConfig `yaml:"adaptive_calibration"`
}

// AdaptiveCalibrationConfig configures the watchdog's adaptive threshold
// calibration system that learns from historical TaskRun observations.
type AdaptiveCalibrationConfig struct {
	Enabled             bool   `yaml:"enabled"`
	MinSampleCount      int    `yaml:"min_sample_count"`     // minimum observations before overriding static defaults (default 10)
	PercentileThreshold string `yaml:"percentile_threshold"` // "p50", "p90", or "p99" (default "p90")
	ColdStartFallback   bool   `yaml:"cold_start_fallback"`  // use static defaults when insufficient data (default true)
	// StorePath is the path for the SQLite-backed calibration profile store.
	// When empty, an in-memory store is used.
	StorePath string `yaml:"store_path,omitempty"`
}

// DiagnosisConfig configures the causal diagnosis subsystem for
// self-healing retry with failure classification.
type DiagnosisConfig struct {
	Enabled             bool `yaml:"enabled"`
	MaxDiagnosesPerTask int  `yaml:"max_diagnoses_per_task"` // maximum diagnoses before terminal failure (default 3)
	EnableEngineSwitch  bool `yaml:"enable_engine_switch"`   // allow diagnosis to recommend engine switches
	// UseLLMClassification enables LLM-backed failure classification with rule-based fallback.
	UseLLMClassification bool `yaml:"use_llm_classification"`
}

// EstimatorConfig configures the predictive cost and duration estimation
// subsystem.
type EstimatorConfig struct {
	Enabled                  bool                     `yaml:"enabled"`
	MaxPredictedCostPerJob   float64                  `yaml:"max_predicted_cost_per_job"` // auto-reject above this (USD)
	DefaultCostPerEngine     map[string]CostRange     `yaml:"default_cost_per_engine"`
	DefaultDurationPerEngine map[string]DurationRange `yaml:"default_duration_per_engine"`
	// StoragePath is the path for the SQLite-backed outcome store.
	// When empty, an in-memory store is used.
	StoragePath string `yaml:"storage_path,omitempty"`
}

// CostRange represents a low/high cost range in USD.
type CostRange struct {
	Low  float64 `yaml:"low"`
	High float64 `yaml:"high"`
}

// DurationRange represents a low/high duration range in minutes.
type DurationRange struct {
	LowMinutes  int `yaml:"low_minutes"`
	HighMinutes int `yaml:"high_minutes"`
}

// RoutingConfig configures the intelligent engine routing subsystem.
type RoutingConfig struct {
	Enabled              bool    `yaml:"enabled"`
	EpsilonGreedy        float64 `yaml:"epsilon_greedy"`          // exploration probability (default 0.1)
	MinSamplesForRouting int     `yaml:"min_samples_for_routing"` // minimum samples before using fingerprints (default 5)
	StorePath            string  `yaml:"store_path,omitempty"`    // path for persistent store (empty = in-memory)
}

// MemoryConfig configures the cross-task episodic memory subsystem.
type MemoryConfig struct {
	Enabled            bool    `yaml:"enabled"`
	StorePath          string  `yaml:"store_path"`           // SQLite database path
	DecayIntervalHours int     `yaml:"decay_interval_hours"` // how often to run confidence decay
	PruneThreshold     float64 `yaml:"prune_threshold"`      // nodes below this confidence are pruned
	MaxFactsPerQuery   int     `yaml:"max_facts_per_query"`  // maximum nodes returned per query
	TenantIsolation    bool    `yaml:"tenant_isolation"`     // enforce strict tenant-scoped queries
	// UseLLMExtraction enables LLM-backed knowledge extraction merged with V1 heuristics.
	UseLLMExtraction bool `yaml:"use_llm_extraction"`
}

// AuditConfig configures audit log storage for task run transcripts.
type AuditConfig struct {
	Transcript TranscriptConfig `yaml:"transcript"`
}

// TranscriptConfig configures where task transcripts are stored.
type TranscriptConfig struct {
	Backend string `yaml:"backend"`          // "local" | "s3" | "gcs" | "disabled"
	Path    string `yaml:"path,omitempty"`   // directory for the local backend
	Bucket  string `yaml:"bucket,omitempty"` // bucket name for s3/gcs
	Prefix  string `yaml:"prefix,omitempty"` // key prefix for s3/gcs
}

// Load reads and parses a Osmia configuration file from the given path.
// It returns an error if the file cannot be read, parsed, or fails validation.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// applyDefaults fills in zero-value fields with sensible defaults. It is
// called after unmarshalling so that users who omit a field get the expected
// default rather than the Go zero value.
func (c *Config) applyDefaults() {
	if c.Routing.EpsilonGreedy == 0 {
		c.Routing.EpsilonGreedy = 0.1
	}

	if c.Engines.ClaudeCode != nil {
		sp := &c.Engines.ClaudeCode.SessionPersistence
		if sp.Enabled {
			if sp.StorageSize == "" {
				sp.StorageSize = "1Gi"
			}
			if sp.S3Prefix == "" {
				sp.S3Prefix = "osmia-sessions/"
			}
			if sp.TTLMinutes == 0 {
				sp.TTLMinutes = 1440
			}
		}
	}
}
