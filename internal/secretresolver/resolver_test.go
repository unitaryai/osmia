package secretresolver

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBackend is a test double for secrets.Backend.
type mockBackend struct {
	name    string
	secrets map[string]string
}

func (m *mockBackend) GetSecret(_ context.Context, key string) (string, error) {
	val, ok := m.secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found", key)
	}
	return val, nil
}

func (m *mockBackend) GetSecrets(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		val, ok := m.secrets[key]
		if !ok {
			return nil, fmt.Errorf("secret %q not found", key)
		}
		result[key] = val
	}
	return result, nil
}

func (m *mockBackend) BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error) {
	envVars := make([]corev1.EnvVar, 0, len(secretRefs))
	for envName, value := range secretRefs {
		envVars = append(envVars, corev1.EnvVar{Name: envName, Value: value})
	}
	return envVars, nil
}

func (m *mockBackend) Name() string          { return m.name }
func (m *mockBackend) InterfaceVersion() int { return 1 }

func TestResolverResolve(t *testing.T) {
	vaultBackend := &mockBackend{
		name: "vault",
		secrets: map[string]string{
			"secret/data/stripe/test-key#api_key": "sk_test_123",
			"secret/data/db#url":                  "postgres://localhost:5432/mydb",
		},
	}

	k8sBackend := &mockBackend{
		name: "k8s",
		secrets: map[string]string{
			"my-secret/token": "ghp_abc123",
		},
	}

	tests := []struct {
		name     string
		requests []SecretRequest
		aliases  map[string]SecretAlias
		policy   Policy
		want     []ResolvedSecret
		wantErr  string
	}{
		{
			name: "resolve vault secret via raw ref",
			requests: []SecretRequest{
				{EnvName: "STRIPE_API_KEY", URI: "vault://secret/data/stripe/test-key#api_key"},
			},
			policy: Policy{AllowRawRefs: true, AllowedSchemes: []string{"vault", "k8s"}},
			want: []ResolvedSecret{
				{EnvName: "STRIPE_API_KEY", Value: "sk_test_123"},
			},
		},
		{
			name: "resolve k8s secret returns SecretKeyRef",
			requests: []SecretRequest{
				{EnvName: "GH_TOKEN", URI: "k8s://my-secret/token"},
			},
			policy: Policy{AllowRawRefs: true, AllowedSchemes: []string{"vault", "k8s"}},
			want: []ResolvedSecret{
				{
					EnvName: "GH_TOKEN",
					SecretKeyRef: &SecretKeyRef{
						SecretName: "my-secret",
						Key:        "token",
					},
				},
			},
		},
		{
			name: "resolve alias",
			requests: []SecretRequest{
				{URI: "alias://stripe-test"},
			},
			aliases: map[string]SecretAlias{
				"stripe-test": {
					Name: "STRIPE_API_KEY",
					URI:  "vault://secret/data/stripe/test-key#api_key",
				},
			},
			policy: Policy{AllowRawRefs: true, AllowedSchemes: []string{"vault", "k8s", "alias"}},
			want: []ResolvedSecret{
				{EnvName: "STRIPE_API_KEY", Value: "sk_test_123"},
			},
		},
		{
			name: "alias with tenant scoping",
			requests: []SecretRequest{
				{URI: "alias://stripe-test"},
			},
			aliases: map[string]SecretAlias{
				"stripe-test": {
					Name:     "STRIPE_API_KEY",
					URI:      "vault://secret/data/stripe/test-key#api_key",
					TenantID: "team-alpha",
				},
			},
			policy: Policy{
				AllowRawRefs:   true,
				AllowedSchemes: []string{"vault", "alias"},
				TenantID:       "team-beta",
			},
			wantErr: "not accessible from tenant",
		},
		{
			name: "policy violation blocks resolution",
			requests: []SecretRequest{
				{EnvName: "PATH", URI: "vault://secret/data/evil#path"},
			},
			policy: Policy{
				AllowRawRefs:       true,
				AllowedSchemes:     []string{"vault"},
				BlockedEnvPatterns: []string{"PATH"},
			},
			wantErr: "policy violation",
		},
		{
			name: "unknown alias",
			requests: []SecretRequest{
				{URI: "alias://nonexistent"},
			},
			policy:  Policy{AllowRawRefs: false, AllowedSchemes: []string{"alias"}},
			wantErr: "unknown secret alias",
		},
		{
			name: "unknown scheme",
			requests: []SecretRequest{
				{EnvName: "MY_SECRET", URI: "aws-sm://my-secret#key"},
			},
			policy:  Policy{AllowRawRefs: true, AllowedSchemes: []string{"vault", "k8s", "aws-sm"}},
			wantErr: "no backend registered for scheme",
		},
		{
			name: "multiple requests resolved together",
			requests: []SecretRequest{
				{EnvName: "STRIPE_API_KEY", URI: "vault://secret/data/stripe/test-key#api_key"},
				{EnvName: "DATABASE_URL", URI: "vault://secret/data/db#url"},
			},
			policy: Policy{AllowRawRefs: true, AllowedSchemes: []string{"vault", "k8s"}},
			want: []ResolvedSecret{
				{EnvName: "STRIPE_API_KEY", Value: "sk_test_123"},
				{EnvName: "DATABASE_URL", Value: "postgres://localhost:5432/mydb"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []Option{
				WithBackend("vault", vaultBackend),
				WithBackend("k8s", k8sBackend),
				WithPolicy(tt.policy),
				WithLogger(slog.Default()),
			}
			if tt.aliases != nil {
				opts = append(opts, WithAliases(tt.aliases))
			}

			resolver := NewResolver(opts...)
			got, err := resolver.Resolve(context.Background(), tt.requests)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseScheme(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"vault://secret/data/db#url", "vault"},
		{"k8s://my-secret/key", "k8s"},
		{"alias://stripe-test", "alias"},
		{"aws-sm://secret-name#key", "aws-sm"},
		{"invalid-uri", ""},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got := parseScheme(tt.uri)
			assert.Equal(t, tt.want, got)
		})
	}
}
