package awssm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSMServer creates an httptest.Server that mimics the AWS Secrets Manager
// JSON-RPC API. It dispatches on the X-Amz-Target header and returns
// SecretString responses for known secrets.
func mockSMServer(t *testing.T, secrets map[string]string, callCount *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		if target != "secretsmanager.GetSecretValue" {
			http.Error(w, `{"__type":"UnknownOperationException"}`, http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var req struct {
			SecretId string `json:"SecretId"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		if callCount != nil {
			callCount.Add(1)
		}

		value, ok := secrets[req.SecretId]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			resp := map[string]string{
				"__type":  "ResourceNotFoundException",
				"Message": "Secrets Manager can't find the specified secret.",
			}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
			return
		}

		resp := map[string]any{
			"ARN":          "arn:aws:secretsmanager:eu-west-1:123456789:secret:" + req.SecretId,
			"Name":         req.SecretId,
			"SecretString": value,
			"VersionId":    "test-version-id",
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

// newTestBackend creates a Backend wired to the given mock server.
func newTestBackend(t *testing.T, server *httptest.Server) *Backend {
	t.Helper()

	smClient := secretsmanager.New(secretsmanager.Options{
		Region:       "eu-west-1",
		BaseEndpoint: aws.String(server.URL),
		HTTPClient:   server.Client(),
		// Use static credentials to avoid SDK trying real credential chain.
		Credentials: aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				Source:          "test",
			}, nil
		}),
	})

	return NewBackend(
		WithSMClient(smClient),
		WithLogger(slog.Default()),
		WithCacheTTL(5*time.Minute),
	)
}

func TestGetSecret_SpecificField(t *testing.T) {
	/*
		Given a JSON-valued secret in AWS SM,
		When GetSecret is called with a key containing a #field suffix,
		Then it should return only the value of that field.
	*/
	secrets := map[string]string{
		"myapp/config": `{"db_host":"db.example.com","db_port":"5432","db_password":"s3cret"}`,
	}
	server := mockSMServer(t, secrets, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "db_host field", key: "myapp/config#db_host", want: "db.example.com"},
		{name: "db_port field", key: "myapp/config#db_port", want: "5432"},
		{name: "db_password field", key: "myapp/config#db_password", want: "s3cret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := backend.GetSecret(context.Background(), tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetSecret_RawString(t *testing.T) {
	/*
		Given a plain-string secret (not JSON),
		When GetSecret is called without a #field suffix,
		Then it should return the raw secret string.
	*/
	secrets := map[string]string{
		"myapp/api-key": "sk-ant-api03-EXAMPLE",
	}
	server := mockSMServer(t, secrets, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	got, err := backend.GetSecret(context.Background(), "myapp/api-key")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-EXAMPLE", got)
}

func TestGetSecret_FullJSON(t *testing.T) {
	/*
		Given a JSON-valued secret,
		When GetSecret is called without a #field suffix,
		Then it should return the full JSON string.
	*/
	jsonValue := `{"db_host":"db.example.com","db_port":"5432"}`
	secrets := map[string]string{
		"myapp/config": jsonValue,
	}
	server := mockSMServer(t, secrets, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	got, err := backend.GetSecret(context.Background(), "myapp/config")
	require.NoError(t, err)
	assert.Equal(t, jsonValue, got)
}

func TestGetSecret_FieldNotFound(t *testing.T) {
	/*
		Given a JSON-valued secret,
		When GetSecret is called with a #field that does not exist,
		Then it should return an error.
	*/
	secrets := map[string]string{
		"myapp/config": `{"db_host":"db.example.com"}`,
	}
	server := mockSMServer(t, secrets, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	_, err := backend.GetSecret(context.Background(), "myapp/config#nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field "nonexistent" not found`)
}

func TestGetSecret_SecretNotFound(t *testing.T) {
	/*
		Given an empty secrets store,
		When GetSecret is called for a non-existent secret,
		Then it should return an error.
	*/
	server := mockSMServer(t, map[string]string{}, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	_, err := backend.GetSecret(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retrieving secret")
}

func TestGetSecret_ARN(t *testing.T) {
	/*
		Given a secret accessible by full ARN,
		When GetSecret is called with an ARN as the secret name,
		Then it should retrieve the secret correctly.
	*/
	arn := "arn:aws:secretsmanager:eu-west-1:123456789:secret:myapp/config"
	secrets := map[string]string{
		arn: `{"api_key":"test-key-123"}`,
	}
	server := mockSMServer(t, secrets, nil)
	defer server.Close()

	backend := newTestBackend(t, server)

	got, err := backend.GetSecret(context.Background(), arn+"#api_key")
	require.NoError(t, err)
	assert.Equal(t, "test-key-123", got)
}

func TestGetSecrets_BatchDedup(t *testing.T) {
	/*
		Given a JSON-valued secret with multiple fields,
		When GetSecrets is called with multiple keys from the same secret,
		Then it should only make one API call (the rest served from cache).
	*/
	var callCount atomic.Int64
	secrets := map[string]string{
		"myapp/config": `{"db_host":"db.example.com","db_port":"5432","db_password":"s3cret"}`,
	}
	server := mockSMServer(t, secrets, &callCount)
	defer server.Close()

	backend := newTestBackend(t, server)

	keys := []string{
		"myapp/config#db_host",
		"myapp/config#db_port",
		"myapp/config#db_password",
	}

	result, err := backend.GetSecrets(context.Background(), keys)
	require.NoError(t, err)

	assert.Equal(t, "db.example.com", result["myapp/config#db_host"])
	assert.Equal(t, "5432", result["myapp/config#db_port"])
	assert.Equal(t, "s3cret", result["myapp/config#db_password"])

	// Only one API call should have been made for the single secret name.
	assert.Equal(t, int64(1), callCount.Load())
}

func TestCache_HitWithinTTL(t *testing.T) {
	/*
		Given a secret that has been fetched,
		When GetSecret is called again within the cache TTL,
		Then it should return the cached value without an API call.
	*/
	var callCount atomic.Int64
	secrets := map[string]string{
		"myapp/key": "value-1",
	}
	server := mockSMServer(t, secrets, &callCount)
	defer server.Close()

	backend := newTestBackend(t, server)

	// First call — fetches from API.
	got1, err := backend.GetSecret(context.Background(), "myapp/key")
	require.NoError(t, err)
	assert.Equal(t, "value-1", got1)
	assert.Equal(t, int64(1), callCount.Load())

	// Second call — should use cache.
	got2, err := backend.GetSecret(context.Background(), "myapp/key")
	require.NoError(t, err)
	assert.Equal(t, "value-1", got2)
	assert.Equal(t, int64(1), callCount.Load())
}

func TestCache_ExpiredTTL(t *testing.T) {
	/*
		Given a secret that was fetched with a very short TTL,
		When GetSecret is called after the TTL has expired,
		Then it should re-fetch from the API.
	*/
	var callCount atomic.Int64
	secrets := map[string]string{
		"myapp/key": "value-1",
	}
	server := mockSMServer(t, secrets, &callCount)
	defer server.Close()

	smClient := secretsmanager.New(secretsmanager.Options{
		Region:       "eu-west-1",
		BaseEndpoint: aws.String(server.URL),
		HTTPClient:   server.Client(),
		Credentials: aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				Source:          "test",
			}, nil
		}),
	})

	backend := NewBackend(
		WithSMClient(smClient),
		WithLogger(slog.Default()),
		WithCacheTTL(1*time.Millisecond),
	)

	// First call.
	_, err := backend.GetSecret(context.Background(), "myapp/key")
	require.NoError(t, err)
	assert.Equal(t, int64(1), callCount.Load())

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	// Second call should re-fetch.
	_, err = backend.GetSecret(context.Background(), "myapp/key")
	require.NoError(t, err)
	assert.Equal(t, int64(2), callCount.Load())
}

func TestBuildEnvVars(t *testing.T) {
	/*
		Given a set of secret references,
		When BuildEnvVars is called,
		Then it should return EnvVars with direct values (not SecretKeyRefs).
	*/
	backend := NewBackend()

	refs := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-EXAMPLE",
		"GITHUB_TOKEN":      "ghp_EXAMPLE",
	}

	envVars, err := backend.BuildEnvVars(refs)
	require.NoError(t, err)
	assert.Len(t, envVars, 2)

	// Build a map for order-independent assertion.
	envMap := make(map[string]string, len(envVars))
	for _, ev := range envVars {
		envMap[ev.Name] = ev.Value
	}
	assert.Equal(t, "sk-ant-EXAMPLE", envMap["ANTHROPIC_API_KEY"])
	assert.Equal(t, "ghp_EXAMPLE", envMap["GITHUB_TOKEN"])
}

func TestMetadata(t *testing.T) {
	/*
		Given a new backend,
		When Name and InterfaceVersion are called,
		Then they should return the correct constants.
	*/
	backend := NewBackend()

	assert.Equal(t, "aws-secrets-manager", backend.Name())
	assert.Equal(t, 1, backend.InterfaceVersion())
}

func TestParseKey(t *testing.T) {
	/*
		Given various key formats,
		When parseKey is called,
		Then it should correctly split the secret name and JSON field.
	*/
	tests := []struct {
		key       string
		wantName  string
		wantField string
	}{
		{key: "myapp/config#db_host", wantName: "myapp/config", wantField: "db_host"},
		{key: "simple-secret", wantName: "simple-secret", wantField: ""},
		{key: "path/to/secret#field", wantName: "path/to/secret", wantField: "field"},
		{
			key:       "arn:aws:secretsmanager:eu-west-1:123456789:secret:myapp/config#api_key",
			wantName:  "arn:aws:secretsmanager:eu-west-1:123456789:secret:myapp/config",
			wantField: "api_key",
		},
		{key: "secret#", wantName: "secret", wantField: ""},
		{key: "#field", wantName: "", wantField: "field"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			name, field := parseKey(tt.key)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantField, field)
		})
	}
}
