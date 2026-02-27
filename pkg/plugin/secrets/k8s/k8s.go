// Package k8s provides a built-in secrets.Backend implementation that reads
// secrets from Kubernetes Secret objects using client-go.
package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/unitaryai/robodev/pkg/plugin/secrets"
)

const backendName = "k8s"

// Compile-time check that K8sBackend implements secrets.Backend.
var _ secrets.Backend = (*K8sBackend)(nil)

// K8sBackend implements secrets.Backend by reading values from Kubernetes
// Secret objects in a configured namespace.
type K8sBackend struct {
	namespace string
	client    kubernetes.Interface
	logger    *slog.Logger
}

// NewK8sBackend creates a new Kubernetes secrets backend.
func NewK8sBackend(namespace string, client kubernetes.Interface, logger *slog.Logger) *K8sBackend {
	return &K8sBackend{
		namespace: namespace,
		client:    client,
		logger:    logger,
	}
}

// GetSecret retrieves a single secret value. The key format is
// "secretName/dataKey" where secretName is the Kubernetes Secret object
// name and dataKey is the key within the Secret's data map.
func (b *K8sBackend) GetSecret(ctx context.Context, key string) (string, error) {
	secretName, dataKey, err := parseSecretKey(key)
	if err != nil {
		return "", err
	}

	secret, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("fetching secret %q: %w", secretName, err)
	}

	value, ok := secret.Data[dataKey]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", dataKey, secretName)
	}

	return string(value), nil
}

// GetSecrets retrieves multiple secret values by key. Each key follows the
// "secretName/dataKey" format.
func (b *K8sBackend) GetSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	// Cache fetched secrets to avoid redundant API calls when multiple
	// keys reference the same Secret object.
	cache := make(map[string]*corev1.Secret)

	for _, key := range keys {
		secretName, dataKey, err := parseSecretKey(key)
		if err != nil {
			return nil, err
		}

		secret, cached := cache[secretName]
		if !cached {
			secret, err = b.client.CoreV1().Secrets(b.namespace).Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("fetching secret %q: %w", secretName, err)
			}
			cache[secretName] = secret
		}

		value, ok := secret.Data[dataKey]
		if !ok {
			return nil, fmt.Errorf("key %q not found in secret %q", dataKey, secretName)
		}
		result[key] = string(value)
	}

	return result, nil
}

// BuildEnvVars translates secret references into Kubernetes EnvVar
// definitions using SecretKeyRef. The secretRefs map is keyed by environment
// variable name; values follow the "secretName/dataKey" format.
func (b *K8sBackend) BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error) {
	envVars := make([]corev1.EnvVar, 0, len(secretRefs))

	for envName, ref := range secretRefs {
		secretName, dataKey, err := parseSecretKey(ref)
		if err != nil {
			return nil, fmt.Errorf("parsing ref for env var %q: %w", envName, err)
		}

		envVars = append(envVars, corev1.EnvVar{
			Name: envName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: dataKey,
				},
			},
		})
	}

	return envVars, nil
}

// Name returns the backend identifier.
func (b *K8sBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the secrets interface version implemented.
func (b *K8sBackend) InterfaceVersion() int {
	return secrets.InterfaceVersion
}

// parseSecretKey splits a key in "secretName/dataKey" format and validates it.
func parseSecretKey(key string) (string, string, error) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid secret key format %q: expected \"secretName/dataKey\"", key)
	}
	return parts[0], parts[1], nil
}
