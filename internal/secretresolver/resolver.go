package secretresolver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/unitaryai/robodev/pkg/plugin/secrets"
)

// Resolver performs multi-backend secret resolution. It validates requests
// against a policy, expands aliases, and dispatches to the appropriate
// secrets backend based on URI scheme.
type Resolver struct {
	backends map[string]secrets.Backend
	aliases  map[string]SecretAlias
	policy   Policy
	logger   *slog.Logger
	audit    *AuditLogger
}

// Option is a functional option for configuring a Resolver.
type Option func(*Resolver)

// WithBackend registers a secrets backend for a given URI scheme.
func WithBackend(scheme string, backend secrets.Backend) Option {
	return func(r *Resolver) {
		r.backends[scheme] = backend
	}
}

// WithAliases sets the alias map for the resolver.
func WithAliases(aliases map[string]SecretAlias) Option {
	return func(r *Resolver) {
		r.aliases = aliases
	}
}

// WithPolicy sets the security policy for the resolver.
func WithPolicy(policy Policy) Option {
	return func(r *Resolver) {
		r.policy = policy
	}
}

// WithLogger sets the structured logger for the resolver.
func WithLogger(logger *slog.Logger) Option {
	return func(r *Resolver) {
		r.logger = logger
		r.audit = NewAuditLogger(logger)
	}
}

// NewResolver creates a new Resolver with the given options.
func NewResolver(opts ...Option) *Resolver {
	r := &Resolver{
		backends: make(map[string]secrets.Backend),
		aliases:  make(map[string]SecretAlias),
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.audit == nil {
		r.audit = NewAuditLogger(r.logger)
	}
	return r
}

// Resolve validates, expands, and resolves a list of secret requests.
// It returns the resolved secrets ready for injection into an execution
// environment.
func (r *Resolver) Resolve(ctx context.Context, requests []SecretRequest) ([]ResolvedSecret, error) {
	// Expand aliases first so that policy validation applies to concrete URIs.
	expanded, err := r.expandAliases(requests)
	if err != nil {
		return nil, fmt.Errorf("expanding aliases: %w", err)
	}

	// Validate each request against policy.
	for _, req := range expanded {
		if err := ValidateRequest(r.policy, req); err != nil {
			return nil, fmt.Errorf("policy violation for %q: %w", req.EnvName, err)
		}
	}

	// Resolve each request via the appropriate backend.
	resolved := make([]ResolvedSecret, 0, len(expanded))
	for _, req := range expanded {
		rs, err := r.resolveOne(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("resolving secret for %q: %w", req.EnvName, err)
		}
		resolved = append(resolved, rs)
	}

	return resolved, nil
}

// expandAliases replaces alias:// URIs with concrete URIs from the alias map.
func (r *Resolver) expandAliases(requests []SecretRequest) ([]SecretRequest, error) {
	var expanded []SecretRequest
	for _, req := range requests {
		scheme := parseScheme(req.URI)
		if scheme != "alias" {
			expanded = append(expanded, req)
			continue
		}

		// Extract alias name from URI (alias://name).
		aliasName := strings.TrimPrefix(req.URI, "alias://")
		alias, ok := r.aliases[aliasName]
		if !ok {
			return nil, fmt.Errorf("unknown secret alias %q", aliasName)
		}

		// Validate tenant scoping.
		if err := ValidateAliasTenant(r.policy, alias); err != nil {
			return nil, err
		}

		// If the original request had an env name, use it; otherwise the
		// alias must provide one via its URI + the caller's context.
		envName := req.EnvName
		if envName == "" {
			// Alias expansion: the env name comes from the alias itself
			// in the configuration layer. For inline alias references,
			// the env name should come from the alias config.
			envName = alias.Name
		}

		expanded = append(expanded, SecretRequest{
			EnvName: envName,
			URI:     alias.URI,
		})
	}
	return expanded, nil
}

// resolveOne resolves a single secret request by dispatching to the
// appropriate backend based on the URI scheme.
func (r *Resolver) resolveOne(ctx context.Context, req SecretRequest) (ResolvedSecret, error) {
	scheme := parseScheme(req.URI)
	if scheme == "" {
		return ResolvedSecret{}, fmt.Errorf("invalid URI %q: missing scheme", req.URI)
	}

	backend, ok := r.backends[scheme]
	if !ok {
		return ResolvedSecret{}, fmt.Errorf("no backend registered for scheme %q", scheme)
	}

	// Parse the key from the URI. The key is everything after "scheme://".
	key := strings.TrimPrefix(req.URI, scheme+"://")

	// For K8s backend, return a SecretKeyRef for native injection.
	if scheme == "k8s" {
		return ResolvedSecret{
			EnvName: req.EnvName,
			SecretKeyRef: &SecretKeyRef{
				SecretName: parseK8sSecretName(key),
				Key:        parseK8sSecretKey(key),
			},
		}, nil
	}

	// For other backends, fetch the value.
	value, err := backend.GetSecret(ctx, key)
	if err != nil {
		return ResolvedSecret{}, fmt.Errorf("backend %q: %w", backend.Name(), err)
	}

	return ResolvedSecret{
		EnvName: req.EnvName,
		Value:   value,
	}, nil
}

// parseK8sSecretName extracts the secret name from a K8s URI key.
// Key format: "secretName/dataKey".
func parseK8sSecretName(key string) string {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) < 1 {
		return ""
	}
	return parts[0]
}

// parseK8sSecretKey extracts the data key from a K8s URI key.
// Key format: "secretName/dataKey".
func parseK8sSecretKey(key string) string {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
