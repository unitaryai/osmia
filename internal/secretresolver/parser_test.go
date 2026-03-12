package secretresolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCommentBlock(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []SecretRequest
		wantErr bool
	}{
		{
			name: "single ref entry",
			body: `Some ticket text.
<!-- osmia:secrets
  - ref: vault://secret/data/stripe/test-key#api_key
    env: STRIPE_API_KEY
-->
More text.`,
			want: []SecretRequest{
				{EnvName: "STRIPE_API_KEY", URI: "vault://secret/data/stripe/test-key#api_key"},
			},
		},
		{
			name: "multiple ref entries",
			body: `<!-- osmia:secrets
  - ref: vault://secret/data/stripe/test-key#api_key
    env: STRIPE_API_KEY
  - ref: k8s://my-secret/token
    env: GH_TOKEN
-->`,
			want: []SecretRequest{
				{EnvName: "STRIPE_API_KEY", URI: "vault://secret/data/stripe/test-key#api_key"},
				{EnvName: "GH_TOKEN", URI: "k8s://my-secret/token"},
			},
		},
		{
			name: "alias entry",
			body: `<!-- osmia:secrets
  - alias: stripe-test
-->`,
			want: []SecretRequest{
				{URI: "alias://stripe-test"},
			},
		},
		{
			name: "mixed alias and ref entries",
			body: `<!-- osmia:secrets
  - alias: stripe-test
  - ref: vault://secret/data/db#url
    env: DATABASE_URL
-->`,
			want: []SecretRequest{
				{URI: "alias://stripe-test"},
				{EnvName: "DATABASE_URL", URI: "vault://secret/data/db#url"},
			},
		},
		{
			name:    "no secret block",
			body:    "Just a normal ticket with no secrets.",
			want:    nil,
			wantErr: false,
		},
		{
			name: "multiple separate blocks",
			body: `<!-- osmia:secrets
  - alias: stripe-test
-->
Some text between blocks.
<!-- osmia:secrets
  - ref: k8s://my-secret/key
    env: MY_KEY
-->`,
			want: []SecretRequest{
				{URI: "alias://stripe-test"},
				{EnvName: "MY_KEY", URI: "k8s://my-secret/key"},
			},
		},
		{
			name: "malformed YAML",
			body: `<!-- osmia:secrets
  this is not valid yaml: [
-->`,
			wantErr: true,
		},
		{
			name: "entry with neither alias nor ref",
			body: `<!-- osmia:secrets
  - env: MISSING_REF
-->`,
			wantErr: true,
		},
		{
			name: "ref without env",
			body: `<!-- osmia:secrets
  - ref: vault://secret/data/db#url
-->`,
			wantErr: true,
		},
		{
			name: "empty comment block",
			body: `<!-- osmia:secrets
-->`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommentBlock(tt.body)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		want    []SecretRequest
		wantErr bool
	}{
		{
			name:   "single secret label",
			labels: []string{"osmia:secret:STRIPE_API_KEY=vault://secret/data/stripe#key"},
			want: []SecretRequest{
				{EnvName: "STRIPE_API_KEY", URI: "vault://secret/data/stripe#key"},
			},
		},
		{
			name: "multiple labels with non-secret labels mixed in",
			labels: []string{
				"bug",
				"osmia:secret:DB_URL=k8s://my-secret/url",
				"priority:high",
				"osmia:secret:API_KEY=vault://secret/data/app#key",
			},
			want: []SecretRequest{
				{EnvName: "DB_URL", URI: "k8s://my-secret/url"},
				{EnvName: "API_KEY", URI: "vault://secret/data/app#key"},
			},
		},
		{
			name:   "no matching labels",
			labels: []string{"bug", "enhancement", "osmia:task:abc"},
			want:   nil,
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   nil,
		},
		{
			name:   "alias URI in label",
			labels: []string{"osmia:secret:STRIPE=alias://stripe-test"},
			want: []SecretRequest{
				{EnvName: "STRIPE", URI: "alias://stripe-test"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLabels(tt.labels)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
