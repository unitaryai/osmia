package k8s

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/robodev/pkg/plugin/secrets"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func seedSecrets(client *fake.Clientset, namespace string) {
	client.CoreV1().Secrets(namespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-keys", Namespace: namespace},
		Data: map[string][]byte{
			"github-token": []byte("ghp_abc123"),
			"openai-key":   []byte("sk-xyz789"),
		},
	}, metav1.CreateOptions{})

	client.CoreV1().Secrets(namespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: namespace},
		Data: map[string][]byte{
			"password": []byte("s3cret"),
		},
	}, metav1.CreateOptions{})
}

func TestK8sBackend_Name(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend("default", client, testLogger())
	assert.Equal(t, "k8s", b.Name())
}

func TestK8sBackend_InterfaceVersion(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend("default", client, testLogger())
	assert.Equal(t, secrets.InterfaceVersion, b.InterfaceVersion())
}

func TestK8sBackend_GetSecret(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    string
		wantErr string
	}{
		{
			name: "valid key",
			key:  "api-keys/github-token",
			want: "ghp_abc123",
		},
		{
			name: "different key same secret",
			key:  "api-keys/openai-key",
			want: "sk-xyz789",
		},
		{
			name: "different secret",
			key:  "db-creds/password",
			want: "s3cret",
		},
		{
			name:    "missing data key",
			key:     "api-keys/nonexistent",
			wantErr: `key "nonexistent" not found in secret "api-keys"`,
		},
		{
			name:    "missing secret",
			key:     "no-such-secret/key",
			wantErr: `fetching secret "no-such-secret"`,
		},
		{
			name:    "invalid format no slash",
			key:     "noslash",
			wantErr: "invalid secret key format",
		},
		{
			name:    "invalid format empty secret name",
			key:     "/datakey",
			wantErr: "invalid secret key format",
		},
		{
			name:    "invalid format empty data key",
			key:     "secretname/",
			wantErr: "invalid secret key format",
		},
	}

	client := fake.NewSimpleClientset()
	seedSecrets(client, "default")
	b := NewK8sBackend("default", client, testLogger())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.GetSecret(context.Background(), tt.key)
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

func TestK8sBackend_GetSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	seedSecrets(client, "test-ns")
	b := NewK8sBackend("test-ns", client, testLogger())

	t.Run("multiple keys from different secrets", func(t *testing.T) {
		keys := []string{"api-keys/github-token", "db-creds/password"}
		result, err := b.GetSecrets(context.Background(), keys)
		require.NoError(t, err)

		assert.Equal(t, "ghp_abc123", result["api-keys/github-token"])
		assert.Equal(t, "s3cret", result["db-creds/password"])
	})

	t.Run("multiple keys from same secret", func(t *testing.T) {
		keys := []string{"api-keys/github-token", "api-keys/openai-key"}
		result, err := b.GetSecrets(context.Background(), keys)
		require.NoError(t, err)

		assert.Len(t, result, 2)
		assert.Equal(t, "ghp_abc123", result["api-keys/github-token"])
		assert.Equal(t, "sk-xyz789", result["api-keys/openai-key"])
	})

	t.Run("empty keys", func(t *testing.T) {
		result, err := b.GetSecrets(context.Background(), []string{})
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("missing key returns error", func(t *testing.T) {
		keys := []string{"api-keys/github-token", "api-keys/nonexistent"}
		_, err := b.GetSecrets(context.Background(), keys)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `key "nonexistent" not found`)
	})
}

func TestK8sBackend_BuildEnvVars(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend("default", client, testLogger())

	t.Run("valid refs", func(t *testing.T) {
		refs := map[string]string{
			"GITHUB_TOKEN": "api-keys/github-token",
			"DB_PASSWORD":  "db-creds/password",
		}
		envVars, err := b.BuildEnvVars(refs)
		require.NoError(t, err)
		require.Len(t, envVars, 2)

		// Sort for deterministic assertions.
		sort.Slice(envVars, func(i, j int) bool {
			return envVars[i].Name < envVars[j].Name
		})

		assert.Equal(t, "DB_PASSWORD", envVars[0].Name)
		require.NotNil(t, envVars[0].ValueFrom)
		require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
		assert.Equal(t, "db-creds", envVars[0].ValueFrom.SecretKeyRef.LocalObjectReference.Name)
		assert.Equal(t, "password", envVars[0].ValueFrom.SecretKeyRef.Key)

		assert.Equal(t, "GITHUB_TOKEN", envVars[1].Name)
		assert.Equal(t, "api-keys", envVars[1].ValueFrom.SecretKeyRef.LocalObjectReference.Name)
		assert.Equal(t, "github-token", envVars[1].ValueFrom.SecretKeyRef.Key)
	})

	t.Run("empty refs", func(t *testing.T) {
		envVars, err := b.BuildEnvVars(map[string]string{})
		require.NoError(t, err)
		assert.Empty(t, envVars)
	})

	t.Run("invalid ref format", func(t *testing.T) {
		refs := map[string]string{
			"BAD_KEY": "noslash",
		}
		_, err := b.BuildEnvVars(refs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid secret key format")
	})
}

func TestK8sBackend_NamespaceIsolation(t *testing.T) {
	client := fake.NewSimpleClientset()
	seedSecrets(client, "ns-a")

	// Backend configured for a different namespace should not see ns-a secrets.
	b := NewK8sBackend("ns-b", client, testLogger())
	_, err := b.GetSecret(context.Background(), "api-keys/github-token")
	require.Error(t, err)
}

func TestParseSecretKey(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantSecret string
		wantData   string
		wantErr    bool
	}{
		{name: "valid", key: "my-secret/my-key", wantSecret: "my-secret", wantData: "my-key"},
		{name: "with nested slash", key: "secret/path/key", wantSecret: "secret", wantData: "path/key"},
		{name: "no slash", key: "noslash", wantErr: true},
		{name: "empty secret", key: "/key", wantErr: true},
		{name: "empty key", key: "secret/", wantErr: true},
		{name: "empty string", key: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret, data, err := parseSecretKey(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSecret, secret)
			assert.Equal(t, tt.wantData, data)
		})
	}
}
