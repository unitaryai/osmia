package secretresolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		policy  Policy
		req     SecretRequest
		wantErr string
	}{
		{
			name: "valid request matching allowed pattern",
			policy: Policy{
				AllowedEnvPatterns: []string{"STRIPE_*", "DATABASE_URL"},
				AllowRawRefs:       true,
				AllowedSchemes:     []string{"vault", "k8s"},
			},
			req: SecretRequest{
				EnvName: "STRIPE_API_KEY",
				URI:     "vault://secret/data/stripe#key",
			},
		},
		{
			name: "blocked env name takes precedence over allowed",
			policy: Policy{
				AllowedEnvPatterns: []string{"*"},
				BlockedEnvPatterns: []string{"PATH", "LD_PRELOAD"},
				AllowRawRefs:       true,
				AllowedSchemes:     []string{"vault"},
			},
			req: SecretRequest{
				EnvName: "PATH",
				URI:     "vault://secret/data/evil#path",
			},
			wantErr: "blocked by pattern",
		},
		{
			name: "env name not in allowed list",
			policy: Policy{
				AllowedEnvPatterns: []string{"STRIPE_*", "DATABASE_URL"},
				AllowRawRefs:       true,
				AllowedSchemes:     []string{"vault"},
			},
			req: SecretRequest{
				EnvName: "RANDOM_KEY",
				URI:     "vault://secret/data/random#key",
			},
			wantErr: "does not match any allowed pattern",
		},
		{
			name: "raw refs disallowed",
			policy: Policy{
				AllowRawRefs: false,
			},
			req: SecretRequest{
				EnvName: "MY_SECRET",
				URI:     "vault://secret/data/mine#key",
			},
			wantErr: "raw secret references are not permitted",
		},
		{
			name: "alias allowed when raw refs disallowed",
			policy: Policy{
				AllowRawRefs:   false,
				AllowedSchemes: []string{"alias"},
			},
			req: SecretRequest{
				URI: "alias://stripe-test",
			},
		},
		{
			name: "scheme not in allowed list",
			policy: Policy{
				AllowRawRefs:   true,
				AllowedSchemes: []string{"k8s"},
			},
			req: SecretRequest{
				EnvName: "DB_URL",
				URI:     "vault://secret/data/db#url",
			},
			wantErr: "not in the allowed schemes list",
		},
		{
			name: "empty allowed schemes permits all schemes",
			policy: Policy{
				AllowRawRefs: true,
			},
			req: SecretRequest{
				EnvName: "MY_SECRET",
				URI:     "vault://secret/data/mine#key",
			},
		},
		{
			name: "empty allowed env patterns permits all env names",
			policy: Policy{
				AllowRawRefs:   true,
				AllowedSchemes: []string{"vault"},
			},
			req: SecretRequest{
				EnvName: "ANYTHING",
				URI:     "vault://secret/data/any#key",
			},
		},
		{
			name: "blocked pattern with wildcard",
			policy: Policy{
				BlockedEnvPatterns: []string{"ROBODEV_*"},
				AllowRawRefs:       true,
			},
			req: SecretRequest{
				EnvName: "ROBODEV_INTERNAL",
				URI:     "vault://secret/data/robodev#key",
			},
			wantErr: "blocked by pattern",
		},
		{
			name: "alias URI with no env name skips env validation",
			policy: Policy{
				AllowedEnvPatterns: []string{"STRIPE_*"},
				AllowRawRefs:       false,
				AllowedSchemes:     []string{"alias"},
			},
			req: SecretRequest{
				URI: "alias://stripe-test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRequest(tt.policy, tt.req)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAliasTenant(t *testing.T) {
	tests := []struct {
		name    string
		policy  Policy
		alias   SecretAlias
		wantErr string
	}{
		{
			name:   "alias available to all tenants",
			policy: Policy{TenantID: "team-alpha"},
			alias:  SecretAlias{Name: "shared-secret"},
		},
		{
			name:   "alias matches tenant",
			policy: Policy{TenantID: "team-alpha"},
			alias: SecretAlias{
				Name:     "stripe-test",
				TenantID: "team-alpha",
			},
		},
		{
			name:   "alias belongs to different tenant",
			policy: Policy{TenantID: "team-alpha"},
			alias: SecretAlias{
				Name:     "stripe-test",
				TenantID: "team-beta",
			},
			wantErr: "not accessible from tenant",
		},
		{
			name:   "tenant-scoped alias with no policy tenant",
			policy: Policy{},
			alias: SecretAlias{
				Name:     "stripe-test",
				TenantID: "team-alpha",
			},
			wantErr: "requires tenant scope but no tenant ID is set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAliasTenant(tt.policy, tt.alias)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
