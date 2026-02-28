// Package secretresolver provides multi-backend secret resolution for
// task-scoped credential injection. It parses secret requirements from
// ticket descriptions, validates them against configurable policies, and
// dispatches to the appropriate secrets backend for resolution.
package secretresolver

// SecretRequest represents a parsed secret requirement from a ticket.
type SecretRequest struct {
	// EnvName is the environment variable name (e.g. "DATABASE_URL").
	EnvName string
	// URI is the secret URI (e.g. "vault://secret/data/db#url", "k8s://my-secret/key", "alias://prod-db").
	URI string
}

// SecretAlias maps a friendly name to a concrete secret URI with optional tenant scoping.
type SecretAlias struct {
	Name      string
	URI       string
	TenantID  string // empty means available to all tenants
	AllowedBy string // policy rule that permitted this alias
}

// ResolvedSecret contains the resolved environment variable for injection.
type ResolvedSecret struct {
	EnvName      string
	Value        string        // only populated for raw value injection (non-K8s paths)
	SecretKeyRef *SecretKeyRef // populated for K8s-native secret injection
}

// SecretKeyRef references a K8s secret key for native injection.
type SecretKeyRef struct {
	SecretName string
	Key        string
}

// Policy controls which secrets can be requested and how.
type Policy struct {
	AllowedEnvPatterns []string // glob patterns for allowed env var names
	BlockedEnvPatterns []string // glob patterns for blocked env var names
	AllowRawRefs       bool     // whether raw (non-alias) URIs are permitted
	AllowedSchemes     []string // allowed URI schemes (e.g. ["k8s", "vault", "alias"])
	TenantID           string   // current tenant scope
}
