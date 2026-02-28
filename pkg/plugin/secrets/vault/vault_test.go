package vault

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestVaultServer creates an httptest.Server that mocks the Vault HTTP API
// for Kubernetes auth and KV v2 reads.
func newTestVaultServer(t *testing.T, kvData map[string]map[string]interface{}) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle Kubernetes auth login.
		if r.URL.Path == "/v1/auth/kubernetes/login" && r.Method == http.MethodPost {
			var payload struct {
				JWT  string `json:"jwt"`
				Role string `json:"role"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if payload.JWT == "" || payload.Role == "" {
				http.Error(w, "missing jwt or role", http.StatusBadRequest)
				return
			}
			resp := map[string]interface{}{
				"auth": map[string]interface{}{
					"client_token": "test-vault-token",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Validate token for secret reads.
		token := r.Header.Get("X-Vault-Token")
		if token != "test-vault-token" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}

		// Handle KV v2 reads. Path format: /v1/{secretsPath}/data/{path}
		// We expect paths starting with /v1/secret/data/
		prefix := "/v1/secret/data/"
		if r.Method == http.MethodGet && len(r.URL.Path) > len(prefix) {
			secretPath := r.URL.Path[len(prefix):]
			data, ok := kvData[secretPath]
			if !ok {
				http.Error(w, "secret not found", http.StatusNotFound)
				return
			}

			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"data": data,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
}

// writeTempSAToken writes a temporary service account token file for testing.
func writeTempSAToken(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	err := os.WriteFile(tokenPath, []byte("test-sa-token"), 0o600)
	require.NoError(t, err)
	return tokenPath
}

func TestVaultBackendGetSecret(t *testing.T) {
	kvData := map[string]map[string]interface{}{
		"stripe/test-key": {
			"api_key":    "sk_test_stripe_123",
			"webhook_id": "whsec_456",
		},
		"database": {
			"url": "postgres://user:pass@host:5432/db",
		},
	}

	server := newTestVaultServer(t, kvData)
	defer server.Close()

	tokenPath := writeTempSAToken(t)

	tests := []struct {
		name    string
		key     string
		want    string
		wantErr string
	}{
		{
			name: "read specific field",
			key:  "stripe/test-key#api_key",
			want: "sk_test_stripe_123",
		},
		{
			name: "read another field from same path",
			key:  "stripe/test-key#webhook_id",
			want: "whsec_456",
		},
		{
			name: "read from different path",
			key:  "database#url",
			want: "postgres://user:pass@host:5432/db",
		},
		{
			name:    "field not found",
			key:     "stripe/test-key#nonexistent",
			wantErr: "field \"nonexistent\" not found",
		},
		{
			name:    "path not found",
			key:     "nonexistent/path#key",
			wantErr: "status 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewVaultBackend(
				WithAddress(server.URL),
				WithRole("test-role"),
				WithAuthMethod("kubernetes"),
				WithSecretsPath("secret"),
				WithHTTPClient(server.Client()),
				WithLogger(slog.Default()),
				withSATokenPath(tokenPath),
			)

			got, err := backend.GetSecret(context.Background(), tt.key)
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

func TestVaultBackendGetSecrets(t *testing.T) {
	kvData := map[string]map[string]interface{}{
		"myapp": {
			"api_key":  "key123",
			"api_salt": "salt456",
		},
	}

	server := newTestVaultServer(t, kvData)
	defer server.Close()

	tokenPath := writeTempSAToken(t)

	backend := NewVaultBackend(
		WithAddress(server.URL),
		WithRole("test-role"),
		withSATokenPath(tokenPath),
		WithHTTPClient(server.Client()),
	)

	keys := []string{"myapp#api_key", "myapp#api_salt"}
	got, err := backend.GetSecrets(context.Background(), keys)
	require.NoError(t, err)
	assert.Equal(t, "key123", got["myapp#api_key"])
	assert.Equal(t, "salt456", got["myapp#api_salt"])
}

func TestVaultBackendGetSecretWithoutFragment(t *testing.T) {
	kvData := map[string]map[string]interface{}{
		"myapp/config": {
			"key1": "value1",
			"key2": "value2",
		},
	}

	server := newTestVaultServer(t, kvData)
	defer server.Close()

	tokenPath := writeTempSAToken(t)

	backend := NewVaultBackend(
		WithAddress(server.URL),
		WithRole("test-role"),
		withSATokenPath(tokenPath),
		WithHTTPClient(server.Client()),
	)

	// Reading without a fragment should return the whole data map as JSON.
	got, err := backend.GetSecret(context.Background(), "myapp/config")
	require.NoError(t, err)

	var data map[string]interface{}
	err = json.Unmarshal([]byte(got), &data)
	require.NoError(t, err)
	assert.Equal(t, "value1", data["key1"])
	assert.Equal(t, "value2", data["key2"])
}

func TestVaultBackendAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
	}))
	defer server.Close()

	tokenPath := writeTempSAToken(t)

	backend := NewVaultBackend(
		WithAddress(server.URL),
		WithRole("bad-role"),
		withSATokenPath(tokenPath),
		WithHTTPClient(server.Client()),
	)

	_, err := backend.GetSecret(context.Background(), "any/path#key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault login failed")
}

func TestVaultBackendMissingSAToken(t *testing.T) {
	backend := NewVaultBackend(
		WithAddress("http://localhost:8200"),
		WithRole("test-role"),
		withSATokenPath("/nonexistent/path/token"),
	)

	_, err := backend.GetSecret(context.Background(), "any/path#key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading service account token")
}

func TestVaultBackendBuildEnvVars(t *testing.T) {
	backend := NewVaultBackend()

	refs := map[string]string{
		"STRIPE_KEY": "sk_test_123",
		"DB_URL":     "postgres://localhost/db",
	}

	envVars, err := backend.BuildEnvVars(refs)
	require.NoError(t, err)
	assert.Len(t, envVars, 2)

	// Convert to a map for order-independent comparison.
	envMap := make(map[string]string)
	for _, ev := range envVars {
		assert.Nil(t, ev.ValueFrom, "vault env vars should use Value, not ValueFrom")
		envMap[ev.Name] = ev.Value
	}
	assert.Equal(t, "sk_test_123", envMap["STRIPE_KEY"])
	assert.Equal(t, "postgres://localhost/db", envMap["DB_URL"])
}

func TestVaultBackendMetadata(t *testing.T) {
	backend := NewVaultBackend()
	assert.Equal(t, "vault", backend.Name())
	assert.Equal(t, 1, backend.InterfaceVersion())
}

func TestParseVaultKey(t *testing.T) {
	tests := []struct {
		key       string
		wantPath  string
		wantField string
	}{
		{"stripe/test-key#api_key", "stripe/test-key", "api_key"},
		{"database#url", "database", "url"},
		{"myapp/config", "myapp/config", ""},
		{"simple", "simple", ""},
		{"path/with/multiple/parts#field", "path/with/multiple/parts", "field"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			gotPath, gotField := parseVaultKey(tt.key)
			assert.Equal(t, tt.wantPath, gotPath)
			assert.Equal(t, tt.wantField, gotField)
		})
	}
}
