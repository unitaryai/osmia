package secretresolver

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateRequest checks a secret request against the given policy.
// It validates the environment variable name against allowed and blocked
// patterns, the URI scheme against allowed schemes, and raw reference
// permissions.
func ValidateRequest(policy Policy, req SecretRequest) error {
	scheme := parseScheme(req.URI)

	// Check raw reference policy. If raw refs are disallowed, only alias://
	// URIs are permitted.
	if !policy.AllowRawRefs && scheme != "alias" {
		return fmt.Errorf("raw secret references are not permitted; use alias:// URIs instead")
	}

	// Validate URI scheme against the allowed list.
	if len(policy.AllowedSchemes) > 0 {
		if !schemeAllowed(scheme, policy.AllowedSchemes) {
			return fmt.Errorf("scheme %q is not in the allowed schemes list", scheme)
		}
	}

	// For alias requests without an env name, skip env name validation
	// (aliases expand to concrete URIs with env names later).
	if req.EnvName == "" {
		return nil
	}

	// Check blocked env name patterns first (blocked takes precedence).
	for _, pattern := range policy.BlockedEnvPatterns {
		matched, err := filepath.Match(pattern, req.EnvName)
		if err != nil {
			return fmt.Errorf("invalid blocked env pattern %q: %w", pattern, err)
		}
		if matched {
			return fmt.Errorf("environment variable %q is blocked by pattern %q", req.EnvName, pattern)
		}
	}

	// Check allowed env name patterns. If the list is non-empty, the env
	// name must match at least one pattern.
	if len(policy.AllowedEnvPatterns) > 0 {
		allowed := false
		for _, pattern := range policy.AllowedEnvPatterns {
			matched, err := filepath.Match(pattern, req.EnvName)
			if err != nil {
				return fmt.Errorf("invalid allowed env pattern %q: %w", pattern, err)
			}
			if matched {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("environment variable %q does not match any allowed pattern", req.EnvName)
		}
	}

	return nil
}

// ValidateAliasTenant checks that a secret alias is accessible to the
// given tenant. If the alias has a non-empty TenantID, it must match the
// policy's TenantID.
func ValidateAliasTenant(policy Policy, alias SecretAlias) error {
	if alias.TenantID == "" {
		// Alias is available to all tenants.
		return nil
	}
	if policy.TenantID == "" {
		return fmt.Errorf("alias %q requires tenant scope but no tenant ID is set in policy", alias.Name)
	}
	if alias.TenantID != policy.TenantID {
		return fmt.Errorf("alias %q belongs to tenant %q, not accessible from tenant %q", alias.Name, alias.TenantID, policy.TenantID)
	}
	return nil
}

// parseScheme extracts the URI scheme from a secret reference URI.
// For example, "vault://secret/data/db#url" returns "vault".
func parseScheme(uri string) string {
	idx := strings.Index(uri, "://")
	if idx < 0 {
		return ""
	}
	return uri[:idx]
}

// schemeAllowed checks whether a given scheme is in the allowed list.
func schemeAllowed(scheme string, allowed []string) bool {
	for _, s := range allowed {
		if s == scheme {
			return true
		}
	}
	return false
}
