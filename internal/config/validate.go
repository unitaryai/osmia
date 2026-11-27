package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateNonNegativeFloat returns an error if v < 0.
func validateNonNegativeFloat(name string, v float64) error {
	if v < 0 {
		return fmt.Errorf("%s must not be negative, got %v", name, v)
	}
	return nil
}

// validateFraction returns an error if v is not in [0.0, 1.0].
func validateFraction(name string, v float64) error {
	if v < 0.0 || v > 1.0 {
		return fmt.Errorf("%s must be between 0.0 and 1.0, got %v", name, v)
	}
	return nil
}

// validateStorePath returns an error if path contains ".." components,
// which could allow directory traversal attacks.
func validateStorePath(name, path string) error {
	// Check with forward slash (always valid on all platforms).
	for _, component := range strings.Split(path, "/") {
		if component == ".." {
			return fmt.Errorf("%s contains directory traversal component %q", name, "..")
		}
	}
	// Also check with the OS path separator if it differs from "/".
	if os.PathSeparator != '/' {
		for _, component := range strings.Split(path, string(os.PathSeparator)) {
			if component == ".." {
				return fmt.Errorf("%s contains directory traversal component %q", name, "..")
			}
		}
	}
	return nil
}

// claudeCodeEngine is the name the Claude Code engine is registered under.
const claudeCodeEngine = "claude-code"

// validateClaudeCodeBedrock requires the bedrock method to name its region and
// every model, since Claude Code's own Bedrock defaults are cross-region, and
// refuses an API key alongside it. Models are refused without Bedrock, where
// they would be ignored, as is a method that is bedrock only once case and
// surrounding whitespace are ignored.
func validateClaudeCodeBedrock(cc *ClaudeCodeEngineConfig) error {
	if cc.Auth.Method != AuthMethodBedrock {
		return validateClaudeCodeNotBedrock(cc)
	}
	required := []struct{ field, value string }{
		{"engines.claude-code.auth.bedrock_region", cc.Auth.BedrockRegion},
		{"engines.claude-code.models.main", cc.Models.Main},
		{"engines.claude-code.models.opus", cc.Models.Opus},
		{"engines.claude-code.models.sonnet", cc.Models.Sonnet},
		{"engines.claude-code.models.haiku", cc.Models.Haiku},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return fmt.Errorf("%s is required when engines.claude-code.auth.method is %q", r.field, AuthMethodBedrock)
		}
	}
	forbidden := []struct{ field, value string }{
		{"engines.claude-code.auth.api_key_secret", cc.Auth.APIKeySecret},
		{"engines.claude-code.auth.api_key_key", cc.Auth.APIKeyKey},
	}
	for _, f := range forbidden {
		if f.value != "" {
			return fmt.Errorf("%s must not be set when engines.claude-code.auth.method is %q", f.field, AuthMethodBedrock)
		}
	}
	return validateClaudeCodeFallbackModel(cc)
}

// validateClaudeCodeFallbackModel requires a fallback model, when set, to be
// one of the pinned Bedrock models, so it cannot leave the region.
func validateClaudeCodeFallbackModel(cc *ClaudeCodeEngineConfig) error {
	m := cc.Models
	switch cc.FallbackModel {
	case "", m.Main, m.Opus, m.Sonnet, m.Haiku:
		return nil
	}
	return fmt.Errorf("engines.claude-code.fallback_model must be one of engines.claude-code.models when engines.claude-code.auth.method is %q", AuthMethodBedrock)
}

// validateClaudeCodeNotBedrock refuses a method that differs from bedrock only
// in case or surrounding whitespace, and the settings only bedrock uses.
func validateClaudeCodeNotBedrock(cc *ClaudeCodeEngineConfig) error {
	if strings.EqualFold(strings.TrimSpace(cc.Auth.Method), AuthMethodBedrock) {
		return fmt.Errorf("engines.claude-code.auth.method %q must be written exactly %q", cc.Auth.Method, AuthMethodBedrock)
	}
	if cc.Models != (ClaudeCodeModels{}) {
		return fmt.Errorf("engines.claude-code.models is only used when engines.claude-code.auth.method is %q", AuthMethodBedrock)
	}
	if cc.Auth.AllowOtherEngines {
		return fmt.Errorf("engines.claude-code.auth.allow_other_engines is only used when engines.claude-code.auth.method is %q", AuthMethodBedrock)
	}
	return nil
}

// validateOnlyClaudeCodeEngine refuses every setting that could give a task
// to an engine other than Claude Code: the default, the fallback chain, the
// tournament judge, the incident triage engine, and any other engine's block,
// since a registered engine can be chosen by a ticket label, by routing or as
// a tournament candidate.
func validateOnlyClaudeCodeEngine(c *Config) error {
	if d := c.Engines.Default; d != "" && d != claudeCodeEngine {
		return otherEngineError("engines.default")
	}
	for i, name := range c.Engines.FallbackEngines {
		if name != claudeCodeEngine {
			return otherEngineError(fmt.Sprintf("engines.fallback_engines[%d]", i))
		}
	}
	ce := c.CompetitiveExecution
	if ce.Enabled && ce.JudgeEngine != "" && ce.JudgeEngine != claudeCodeEngine {
		return otherEngineError("competitive_execution.judge_engine")
	}
	if e := c.IncidentTriage.Engine; e != "" && e != claudeCodeEngine {
		return otherEngineError("incident_triage.engine")
	}
	blocks := []struct {
		field string
		set   bool
	}{
		{"engines.aider", c.Engines.Aider != nil},
		{"engines.cline", c.Engines.Cline != nil},
		{"engines.codex", c.Engines.Codex != nil},
		{"engines.opencode", c.Engines.OpenCode != nil},
	}
	for _, b := range blocks {
		if b.set {
			return otherEngineError(b.field)
		}
	}
	return nil
}

// otherEngineError refuses field, which could give a task to an engine that
// calls its own provider outside the Bedrock region.
func otherEngineError(field string) error {
	return fmt.Errorf("%s lets an engine other than %q take a task outside the Bedrock region, which is refused when engines.claude-code.auth.method is %q unless engines.claude-code.auth.allow_other_engines is true", field, claudeCodeEngine, AuthMethodBedrock)
}

// trimClaudeCodeModelSettings trims the region and model names in place, so
// the values validated are the values the agent is given.
func trimClaudeCodeModelSettings(cc *ClaudeCodeEngineConfig) {
	cc.AgentTeams.TeammateModel = strings.TrimSpace(cc.AgentTeams.TeammateModel)
	cc.Auth.BedrockRegion = strings.TrimSpace(cc.Auth.BedrockRegion)
	cc.FallbackModel = strings.TrimSpace(cc.FallbackModel)
	cc.Models.Main = strings.TrimSpace(cc.Models.Main)
	cc.Models.Opus = strings.TrimSpace(cc.Models.Opus)
	cc.Models.Sonnet = strings.TrimSpace(cc.Models.Sonnet)
	cc.Models.Haiku = strings.TrimSpace(cc.Models.Haiku)
}

// Validate checks all configuration fields for correctness.
// It is called automatically by Load after unmarshalling.
func (c *Config) Validate() error {
	// Reject malformed agent pod annotations at startup. Otherwise the first
	// sign would be every agent Job failing to create.
	if errs := apivalidation.ValidateAnnotations(c.Execution.PodAnnotations, field.NewPath("execution", "pod_annotations")); len(errs) > 0 {
		return errs.ToAggregate()
	}

	if name := c.Execution.ServiceAccountName; name != "" {
		if msgs := apivalidation.ValidateServiceAccountName(name, false); len(msgs) > 0 {
			return field.Invalid(field.NewPath("execution", "service_account_name"), name, strings.Join(msgs, "; "))
		}
	}

	if err := validateNonNegativeFloat("guardrails.max_job_duration_minutes", float64(c.GuardRails.MaxJobDurationMinutes)); err != nil {
		return err
	}

	if err := validateNonNegativeFloat("guardrails.max_cost_per_job", c.GuardRails.MaxCostPerJob); err != nil {
		return err
	}

	if c.Routing.EpsilonGreedy != 0 {
		if err := validateFraction("routing.epsilon_greedy", c.Routing.EpsilonGreedy); err != nil {
			return err
		}
	}

	if c.Memory.PruneThreshold != 0 {
		if err := validateFraction("memory.prune_threshold", c.Memory.PruneThreshold); err != nil {
			return err
		}
	}

	if c.CompetitiveExecution.DefaultCandidates != 0 && c.CompetitiveExecution.DefaultCandidates < 2 {
		return fmt.Errorf("competitive_execution.default_candidates must be 0 or >= 2")
	}

	if c.PRM.HintFilePath != "" {
		if err := validateStorePath("prm.hint_file_path", c.PRM.HintFilePath); err != nil {
			return err
		}
	}

	if c.Routing.StorePath != "" {
		if err := validateStorePath("routing.store_path", c.Routing.StorePath); err != nil {
			return err
		}
	}

	if c.Memory.StorePath != "" {
		if err := validateStorePath("memory.store_path", c.Memory.StorePath); err != nil {
			return err
		}
	}

	if _, ok := c.Ticketing.Config["task_file"]; ok {
		return fmt.Errorf("ticketing.config.task_file is no longer supported; use ticketing.backend=local with ticketing.config.store_path and optional ticketing.config.seed_file")
	}

	if c.Ticketing.Backend == "local" {
		storePath, ok := c.Ticketing.Config["store_path"].(string)
		if !ok || storePath == "" {
			return fmt.Errorf("ticketing.config.store_path is required when ticketing.backend is %q", c.Ticketing.Backend)
		}
		if err := validateStorePath("ticketing.config.store_path", storePath); err != nil {
			return err
		}
		if rawSeedFile, exists := c.Ticketing.Config["seed_file"]; exists {
			seedFile, ok := rawSeedFile.(string)
			if !ok {
				return fmt.Errorf("ticketing.config.seed_file must be a string")
			}
			if seedFile != "" {
				if err := validateStorePath("ticketing.config.seed_file", seedFile); err != nil {
					return err
				}
			}
		}
	}

	if c.Engines.ClaudeCode != nil {
		sp := c.Engines.ClaudeCode.SessionPersistence
		if sp.Enabled {
			switch sp.Backend {
			case "shared-pvc", "per-taskrun-pvc":
				// supported
			case "s3":
				return fmt.Errorf("engines.claude-code.session_persistence.backend %q is not yet supported; use \"shared-pvc\" or \"per-taskrun-pvc\"", sp.Backend)
			case "":
				return fmt.Errorf("engines.claude-code.session_persistence.backend must be set when session persistence is enabled")
			default:
				return fmt.Errorf("engines.claude-code.session_persistence.backend %q is not recognised; use \"shared-pvc\" or \"per-taskrun-pvc\"", sp.Backend)
			}
		}
	}

	if c.ReviewResponse.Enabled && c.ReviewResponse.MinSeverity != "" {
		switch c.ReviewResponse.MinSeverity {
		case "info", "warning", "error":
			// valid
		default:
			return fmt.Errorf("review_response.min_severity must be one of info|warning|error, got %q", c.ReviewResponse.MinSeverity)
		}
	}

	for i, pat := range c.ReviewResponse.IgnoreSummaryAuthors {
		if strings.TrimSpace(pat) == "" {
			return fmt.Errorf("review_response.ignore_summary_authors[%d] must not be blank", i)
		}
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("review_response.ignore_summary_authors[%d] is not a valid regex: %w", i, err)
		}
	}

	if p := c.SCM.BranchPrefix; p != "" {
		if strings.ContainsAny(p, " \t\n\r~^:?*[\\") {
			return fmt.Errorf("scm.branch_prefix contains invalid git ref characters: %q", p)
		}
	}

	if cc := c.Engines.ClaudeCode; cc != nil {
		trimClaudeCodeModelSettings(cc)
		if err := validateClaudeCodeBedrock(cc); err != nil {
			return err
		}
		if cc.Auth.Method == AuthMethodBedrock && !cc.Auth.AllowOtherEngines {
			if err := validateOnlyClaudeCodeEngine(c); err != nil {
				return err
			}
		}
	}

	if g := c.Webhook.Generic; g != nil {
		switch g.AuthMode {
		case "hmac", "bearer", "svix":
			// valid
		case "":
			return fmt.Errorf("webhook.generic.auth_mode is required (must be \"hmac\", \"bearer\", or \"svix\")")
		default:
			return fmt.Errorf("webhook.generic.auth_mode %q is not supported (must be \"hmac\", \"bearer\", or \"svix\")", g.AuthMode)
		}
		if g.Secret == "" {
			return fmt.Errorf("webhook.generic.secret is required")
		}
	}

	switch c.TaskRunStore.Backend {
	case "", "memory", "sqlite":
		// supported
	case "postgres":
		return fmt.Errorf("taskrun_store.backend %q is not yet supported; use \"memory\" or \"sqlite\"", c.TaskRunStore.Backend)
	default:
		return fmt.Errorf("taskrun_store.backend %q is not recognised; use \"memory\" or \"sqlite\"", c.TaskRunStore.Backend)
	}
	if c.TaskRunStore.Backend == "sqlite" && c.TaskRunStore.SQLite.Path != "" {
		if !filepath.IsAbs(c.TaskRunStore.SQLite.Path) {
			return fmt.Errorf("taskrun_store.sqlite.path must be an absolute path, got %q", c.TaskRunStore.SQLite.Path)
		}
	}

	if i := c.Webhook.IncidentIO; i != nil {
		switch i.AuthMode {
		case "svix":
			// valid
		case "":
			return fmt.Errorf("webhook.incident_io.auth_mode is required (must be \"svix\")")
		default:
			return fmt.Errorf("webhook.incident_io.auth_mode %q is not supported (must be \"svix\")", i.AuthMode)
		}
		if i.Secret == "" {
			return fmt.Errorf("webhook.incident_io.secret is required")
		}
	}

	return nil
}
