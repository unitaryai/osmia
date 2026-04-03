// Package engine defines the ExecutionEngine interface and associated types
// used to describe units of work to be performed by AI coding agents.
// Engines are responsible for translating tasks into engine-agnostic
// ExecutionSpecs, which the core JobBuilder then translates into
// Kubernetes Jobs or other runtime constructs.
package engine

import "context"

// TokenUsage tracks token consumption for cost accounting.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// TaskResult is the structured result written by the engine to /workspace/result.json.
type TaskResult struct {
	Success         bool        `json:"success"`
	MergeRequestURL string      `json:"merge_request_url,omitempty"`
	BranchName      string      `json:"branch_name,omitempty"`
	Summary         string      `json:"summary"`
	TokenUsage      *TokenUsage `json:"token_usage,omitempty"`
	CostEstimateUSD float64     `json:"cost_estimate_usd,omitempty"`
	ExitCode        int         `json:"exit_code"` // 0=success, 1=agent failure, 2=guard rail blocked
}

// Resources describes CPU and memory requirements.
type Resources struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// VolumeMount describes a volume to mount into the execution container.
// When ConfigMapName is set, the volume uses a ConfigMap source instead of
// the default emptyDir. SubPath allows mounting a single key as a specific
// filename without shadowing the mount directory.
type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
	// SubPath mounts a single entry from the volume instead of the whole volume.
	SubPath string `json:"sub_path,omitempty"`
	// ConfigMapName, when set, uses a ConfigMap as the volume source.
	ConfigMapName string `json:"configmap_name,omitempty"`
	// ConfigMapKey, when set alongside ConfigMapName, projects only this key.
	ConfigMapKey string `json:"configmap_key,omitempty"`
	// PVCName, when set, uses a PersistentVolumeClaim as the volume source.
	// Takes precedence over ConfigMapName when both are set.
	PVCName string `json:"pvc_name,omitempty"`
}

// Task represents a unit of work to be performed by an engine.
type Task struct {
	ID       string `json:"id"`
	TicketID string `json:"ticket_id"`
	// TaskRunID uniquely identifies this execution attempt. Multiple retries
	// of the same ticket produce different TaskRunIDs. Used by the session
	// store to isolate per-run storage paths.
	TaskRunID   string `json:"task_run_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RepoURL     string `json:"repo_url"`
	// TicketURL is the web URL of the originating ticket (e.g. Shortcut story
	// URL). Used in the prompt to instruct the agent to reference the ticket
	// in merge request titles and descriptions.
	TicketURL string            `json:"ticket_url,omitempty"`
	Labels    []string          `json:"labels,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	// MemoryContext is pre-formatted prior knowledge from episodic memory,
	// injected into the prompt when memory is enabled.
	MemoryContext string `json:"memory_context,omitempty"`
	// PriorBranchName, when non-empty, tells the engine that a previous
	// attempt already pushed work to this branch. The prompt should
	// instruct the agent to clone/checkout that branch and continue from
	// where the prior run left off. Ignored when SessionID is set (session
	// persistence handles context resumption via --resume).
	PriorBranchName string `json:"prior_branch_name,omitempty"`
	// PriorMergeRequestURL, when non-empty, tells the engine that a previous
	// attempt already opened a merge request at this URL. The prompt should
	// instruct the agent to push additional commits to the existing branch
	// rather than creating a new MR.
	PriorMergeRequestURL string `json:"prior_merge_request_url,omitempty"`
	// SessionID, when non-empty, tells the engine to resume the named Claude
	// Code session via --resume. Set by the controller on retry jobs when
	// session persistence is enabled.
	SessionID string `json:"session_id,omitempty"`
}

// EngineConfig holds engine-specific configuration.
type EngineConfig struct {
	Image              string            `json:"image"`
	ResourceRequests   Resources         `json:"resource_requests"`
	ResourceLimits     Resources         `json:"resource_limits"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	Env                map[string]string `json:"env,omitempty"`
	FallbackModel      string            `json:"fallback_model,omitempty"`
	ToolWhitelist      []string          `json:"tool_whitelist,omitempty"`
	ToolBlacklist      []string          `json:"tool_blacklist,omitempty"`
	JSONSchema         string            `json:"json_schema,omitempty"`
	AppendSystemPrompt string            `json:"append_system_prompt,omitempty"`
	// StreamingEnabled enables streaming output mode (stream-json) even
	// without a JSON schema. When true, the engine uses --output-format
	// stream-json and --verbose for richer event data.
	StreamingEnabled bool `json:"streaming_enabled,omitempty"`
	// SecretKeyRefs maps env var names to specific Kubernetes Secret keys to
	// inject into the agent container. Entries are merged into the
	// ExecutionSpec.SecretKeyRefs alongside any engine-level defaults.
	SecretKeyRefs map[string]SecretKeyRef `json:"secret_key_refs,omitempty"`
	// APIKeySecret is the name of the Kubernetes Secret containing the engine's
	// API key (e.g. ANTHROPIC_API_KEY). Overrides any engine-level default.
	APIKeySecret string `json:"api_key_secret,omitempty"`
}

// SecretKeyRef identifies a specific key within a Kubernetes Secret.
// It is used in ExecutionSpec.SecretKeyRefs to map an environment variable
// name to an explicit key inside a named secret, enabling support for secrets
// whose key names differ from the desired environment variable names.
type SecretKeyRef struct {
	// SecretName is the name of the Kubernetes Secret.
	SecretName string `json:"secret_name"`
	// Key is the key within the Secret whose value will be injected.
	Key string `json:"key"`
}

// ExecutionSpec is an engine-agnostic description of what to run.
// The core JobBuilder translates this into a K8s Job (or Docker run, etc).
type ExecutionSpec struct {
	Image   string            `json:"image"`
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
	// SecretEnv maps env var names to Kubernetes Secret names. The entire
	// named secret is mounted via envFrom (all keys become env vars). Use
	// SecretKeyRefs when the secret key name differs from the env var name.
	SecretEnv map[string]string `json:"secret_env,omitempty"`
	// SecretKeyRefs maps env var names to specific keys within Kubernetes
	// Secrets, generating env[].valueFrom.secretKeyRef entries.
	SecretKeyRefs         map[string]SecretKeyRef `json:"secret_key_refs,omitempty"`
	ResourceRequests      Resources               `json:"resource_requests"`
	ResourceLimits        Resources               `json:"resource_limits"`
	Volumes               []VolumeMount           `json:"volumes"`
	ActiveDeadlineSeconds int                     `json:"active_deadline_seconds"`
}

// SessionStore manages persistent storage for agent session state between pod
// restarts. Implementations live in internal/sessionstore; the interface is
// defined here so that public engine packages (pkg/engine/claudecode) can
// accept it without importing internal packages.
type SessionStore interface {
	// Prepare sets up storage for a TaskRun (e.g. creates a per-TaskRun PVC
	// or ensures the S3 prefix exists). Must be called before the first job.
	Prepare(ctx context.Context, taskRunID string) error

	// VolumeMounts returns the additional volume mounts to add to the agent
	// pod spec.
	VolumeMounts(taskRunID string) []VolumeMount

	// Env returns extra environment variables for the agent container.
	Env(taskRunID, sessionID string) map[string]string

	// Cleanup removes storage for a completed TaskRun. Safe to call multiple
	// times; implementations must be idempotent.
	Cleanup(ctx context.Context, taskRunID string) error
}

// ExecutionEngine wraps an AI coding tool (Claude Code, Codex, etc).
type ExecutionEngine interface {
	// BuildExecutionSpec returns a runtime-agnostic spec; the core JobBuilder
	// handles translation to K8s Jobs, Docker containers, etc.
	BuildExecutionSpec(task Task, config EngineConfig) (*ExecutionSpec, error)

	// BuildPrompt constructs the task prompt for this engine.
	BuildPrompt(task Task) (string, error)

	// Name returns a unique engine identifier (e.g. "claude-code", "codex").
	Name() string

	// InterfaceVersion returns the version this engine implements.
	InterfaceVersion() int
}
