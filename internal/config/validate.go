package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
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

// Validate checks all configuration fields for correctness.
// It is called automatically by Load after unmarshalling.
func (c *Config) Validate() error {
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
		if m := strings.TrimSpace(cc.AgentTeams.TeammateModel); m != "" {
			cc.AgentTeams.TeammateModel = m
		}
	}

	return nil
}
