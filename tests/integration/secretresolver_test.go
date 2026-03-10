//go:build integration

package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/secretresolver"
)

// TestSecretResolverHTMLCommentParsing verifies that a <!-- osmia:secrets -->
// HTML comment block embedded in a ticket description is correctly parsed into
// a SecretRequest with the expected EnvName and URI fields.
func TestSecretResolverHTMLCommentParsing(t *testing.T) {
	body := "Some description text.\n" +
		"<!-- osmia:secrets\n" +
		"- ref: vault://secret/path\n" +
		"  env: MY_SECRET\n" +
		"-->\n" +
		"More text."

	requests, err := secretresolver.ParseCommentBlock(body)
	require.NoError(t, err)
	require.Len(t, requests, 1)

	req := requests[0]
	assert.Equal(t, "MY_SECRET", req.EnvName)
	assert.Equal(t, "vault://secret/path", req.URI)
}

// TestSecretResolverHTMLCommentParsing_MultipleEntries verifies that a block
// containing multiple secret entries is parsed into the correct number of requests.
func TestSecretResolverHTMLCommentParsing_MultipleEntries(t *testing.T) {
	body := "<!-- osmia:secrets\n" +
		"- ref: k8s://ns/secret/key\n" +
		"  env: DB_URL\n" +
		"- ref: vault://kv/data/token\n" +
		"  env: API_TOKEN\n" +
		"-->"

	requests, err := secretresolver.ParseCommentBlock(body)
	require.NoError(t, err)
	require.Len(t, requests, 2)

	assert.Equal(t, "DB_URL", requests[0].EnvName)
	assert.Equal(t, "k8s://ns/secret/key", requests[0].URI)

	assert.Equal(t, "API_TOKEN", requests[1].EnvName)
	assert.Equal(t, "vault://kv/data/token", requests[1].URI)
}

// TestSecretResolverLabelParsing verifies that osmia:secret:ENV=URI labels
// are parsed correctly and that unrelated labels are silently ignored.
func TestSecretResolverLabelParsing(t *testing.T) {
	labels := []string{
		"osmia:secret:API_KEY=k8s://ns/secret/key",
		"other-label",
		"osmia",
	}

	requests, err := secretresolver.ParseLabels(labels)
	require.NoError(t, err)
	require.Len(t, requests, 1, "only the osmia:secret label should produce a request")

	req := requests[0]
	assert.Equal(t, "API_KEY", req.EnvName)
	assert.Equal(t, "k8s://ns/secret/key", req.URI)
}

// TestSecretResolverPolicyBlocksRawRefs verifies that when AllowRawRefs is
// false, any SecretRequest referencing a non-alias URI is rejected.
func TestSecretResolverPolicyBlocksRawRefs(t *testing.T) {
	policy := secretresolver.Policy{
		AllowRawRefs: false,
	}

	t.Run("raw vault ref is blocked", func(t *testing.T) {
		req := secretresolver.SecretRequest{
			EnvName: "MY_SECRET",
			URI:     "vault://secret/path",
		}
		err := secretresolver.ValidateRequest(policy, req)
		require.Error(t, err, "raw vault ref should be rejected when AllowRawRefs=false")
	})

	t.Run("raw k8s ref is blocked", func(t *testing.T) {
		req := secretresolver.SecretRequest{
			EnvName: "DB_URL",
			URI:     "k8s://my-namespace/my-secret/key",
		}
		err := secretresolver.ValidateRequest(policy, req)
		require.Error(t, err, "raw k8s ref should be rejected when AllowRawRefs=false")
	})

	t.Run("alias ref is permitted", func(t *testing.T) {
		req := secretresolver.SecretRequest{
			URI: "alias://prod-db",
		}
		err := secretresolver.ValidateRequest(policy, req)
		assert.NoError(t, err, "alias:// URI should always be permitted")
	})
}

// TestSecretResolverPolicyBlocksBlockedEnvNames verifies that environment
// variable names matching a BlockedEnvPatterns glob are rejected, whilst
// names that do not match any blocked pattern are permitted.
func TestSecretResolverPolicyBlocksBlockedEnvNames(t *testing.T) {
	policy := secretresolver.Policy{
		AllowRawRefs:       true,
		BlockedEnvPatterns: []string{"PATH", "LD_*"},
	}

	tests := []struct {
		envName   string
		wantError bool
	}{
		// Exact match — blocked.
		{"PATH", true},
		// Wildcard match — blocked.
		{"LD_PRELOAD", true},
		{"LD_LIBRARY_PATH", true},
		// No match — permitted.
		{"MY_VAR", false},
		{"API_TOKEN", false},
		{"DATABASE_URL", false},
	}

	for _, tt := range tests {
		t.Run(tt.envName, func(t *testing.T) {
			req := secretresolver.SecretRequest{
				EnvName: tt.envName,
				URI:     "vault://secret/data/val",
			}
			err := secretresolver.ValidateRequest(policy, req)
			if tt.wantError {
				assert.Error(t, err, "env name %q should be blocked", tt.envName)
			} else {
				assert.NoError(t, err, "env name %q should be permitted", tt.envName)
			}
		})
	}
}
