package config

import (
	"fmt"
	"os"
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

	if c.ReviewResponse.Enabled && c.ReviewResponse.MinSeverity != "" {
		switch c.ReviewResponse.MinSeverity {
		case "info", "warning", "error":
			// valid
		default:
			return fmt.Errorf("review_response.min_severity must be one of info|warning|error, got %q", c.ReviewResponse.MinSeverity)
		}
	}

	return nil
}
