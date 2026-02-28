// Package vault provides a secrets.Backend implementation that reads secrets
// from HashiCorp Vault using its HTTP API. It supports Kubernetes auth for
// pod-based authentication and KV v2 for secret storage.
package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"

	"github.com/unitaryai/robodev/pkg/plugin/secrets"
)

const backendName = "vault"

// Default path for the Kubernetes service account token.
const defaultSATokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Compile-time check that VaultBackend implements secrets.Backend.
var _ secrets.Backend = (*VaultBackend)(nil)

// VaultBackend implements secrets.Backend by reading secrets from HashiCorp
// Vault via its HTTP API with Kubernetes authentication.
type VaultBackend struct {
	address     string
	role        string
	authMethod  string
	secretsPath string
	httpClient  *http.Client
	logger      *slog.Logger
	saTokenPath string

	mu    sync.Mutex
	token string // cached Vault client token
}

// VaultOption is a functional option for configuring a VaultBackend.
type VaultOption func(*VaultBackend)

// WithAddress sets the Vault server address.
func WithAddress(address string) VaultOption {
	return func(v *VaultBackend) {
		v.address = address
	}
}

// WithRole sets the Vault auth role name.
func WithRole(role string) VaultOption {
	return func(v *VaultBackend) {
		v.role = role
	}
}

// WithAuthMethod sets the Vault authentication method (e.g. "kubernetes").
func WithAuthMethod(method string) VaultOption {
	return func(v *VaultBackend) {
		v.authMethod = method
	}
}

// WithSecretsPath sets the base path for KV v2 secrets in Vault.
func WithSecretsPath(path string) VaultOption {
	return func(v *VaultBackend) {
		v.secretsPath = path
	}
}

// WithHTTPClient sets a custom HTTP client for Vault API calls.
func WithHTTPClient(client *http.Client) VaultOption {
	return func(v *VaultBackend) {
		v.httpClient = client
	}
}

// WithLogger sets the structured logger.
func WithLogger(logger *slog.Logger) VaultOption {
	return func(v *VaultBackend) {
		v.logger = logger
	}
}

// withSATokenPath sets a custom service account token path (for testing).
func withSATokenPath(path string) VaultOption {
	return func(v *VaultBackend) {
		v.saTokenPath = path
	}
}

// NewVaultBackend creates a new Vault secrets backend with the given options.
func NewVaultBackend(opts ...VaultOption) *VaultBackend {
	v := &VaultBackend{
		address:     "http://127.0.0.1:8200",
		authMethod:  "kubernetes",
		secretsPath: "secret",
		httpClient:  http.DefaultClient,
		logger:      slog.Default(),
		saTokenPath: defaultSATokenPath,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// GetSecret retrieves a single secret value from Vault. The key format is
// "path#field" where path is relative to the configured secrets path and
// field is the key within the KV v2 data map. If no fragment is provided,
// the entire JSON data map is returned as a string.
func (v *VaultBackend) GetSecret(ctx context.Context, key string) (string, error) {
	path, field := parseVaultKey(key)

	if err := v.ensureAuthenticated(ctx); err != nil {
		return "", fmt.Errorf("vault authentication: %w", err)
	}

	data, err := v.readKV2(ctx, path)
	if err != nil {
		return "", err
	}

	if field == "" {
		// Return the entire data map as JSON.
		raw, err := json.Marshal(data)
		if err != nil {
			return "", fmt.Errorf("marshalling secret data: %w", err)
		}
		return string(raw), nil
	}

	value, ok := data[field]
	if !ok {
		return "", fmt.Errorf("field %q not found in secret at path %q", field, path)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q in secret at path %q is not a string", field, path)
	}
	return str, nil
}

// GetSecrets retrieves multiple secret values from Vault. Each key follows
// the "path#field" format. Results are cached by path to avoid redundant
// API calls when multiple keys reference the same secret.
func (v *VaultBackend) GetSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	if err := v.ensureAuthenticated(ctx); err != nil {
		return nil, fmt.Errorf("vault authentication: %w", err)
	}

	result := make(map[string]string, len(keys))
	cache := make(map[string]map[string]interface{})

	for _, key := range keys {
		path, field := parseVaultKey(key)

		data, cached := cache[path]
		if !cached {
			var err error
			data, err = v.readKV2(ctx, path)
			if err != nil {
				return nil, err
			}
			cache[path] = data
		}

		if field == "" {
			raw, err := json.Marshal(data)
			if err != nil {
				return nil, fmt.Errorf("marshalling secret data: %w", err)
			}
			result[key] = string(raw)
			continue
		}

		value, ok := data[field]
		if !ok {
			return nil, fmt.Errorf("field %q not found in secret at path %q", field, path)
		}
		str, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("field %q in secret at path %q is not a string", field, path)
		}
		result[key] = str
	}

	return result, nil
}

// BuildEnvVars translates secret references into Kubernetes EnvVar
// definitions. Since Vault secrets are fetched at resolution time (not
// natively injected via K8s), the values are set directly on the EnvVar.
func (v *VaultBackend) BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error) {
	envVars := make([]corev1.EnvVar, 0, len(secretRefs))
	for envName, value := range secretRefs {
		envVars = append(envVars, corev1.EnvVar{
			Name:  envName,
			Value: value,
		})
	}
	return envVars, nil
}

// Name returns the backend identifier.
func (v *VaultBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the secrets interface version implemented.
func (v *VaultBackend) InterfaceVersion() int {
	return secrets.InterfaceVersion
}

// ensureAuthenticated performs Kubernetes auth against Vault if no token
// is cached.
func (v *VaultBackend) ensureAuthenticated(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.token != "" {
		return nil
	}

	if v.authMethod != "kubernetes" {
		return fmt.Errorf("unsupported auth method %q", v.authMethod)
	}

	saToken, err := os.ReadFile(v.saTokenPath)
	if err != nil {
		return fmt.Errorf("reading service account token: %w", err)
	}

	loginPayload := fmt.Sprintf(`{"jwt":%q,"role":%q}`, string(saToken), v.role)

	loginURL := fmt.Sprintf("%s/v1/auth/kubernetes/login", strings.TrimRight(v.address, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(loginPayload))
	if err != nil {
		return fmt.Errorf("creating login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vault login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vault login failed with status %d: %s", resp.StatusCode, string(body))
	}

	var loginResp struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("decoding vault login response: %w", err)
	}

	if loginResp.Auth.ClientToken == "" {
		return fmt.Errorf("vault login returned empty client token")
	}

	v.token = loginResp.Auth.ClientToken
	v.logger.Info("authenticated with vault", "auth_method", v.authMethod)
	return nil
}

// readKV2 reads a KV v2 secret from Vault at the given path and returns
// the inner data map.
func (v *VaultBackend) readKV2(ctx context.Context, path string) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/v1/%s/data/%s", strings.TrimRight(v.address, "/"), v.secretsPath, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating vault read request: %w", err)
	}

	v.mu.Lock()
	token := v.token
	v.mu.Unlock()

	req.Header.Set("X-Vault-Token", token)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault read request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("vault read failed with status %d: %s", resp.StatusCode, string(body))
	}

	var kvResp struct {
		Data struct {
			Data map[string]interface{} `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&kvResp); err != nil {
		return nil, fmt.Errorf("decoding vault secret response: %w", err)
	}

	if kvResp.Data.Data == nil {
		return nil, fmt.Errorf("vault returned nil data for path %q", path)
	}

	return kvResp.Data.Data, nil
}

// parseVaultKey splits a key in "path#field" format. If no fragment is
// present, the field is empty.
func parseVaultKey(key string) (path, field string) {
	idx := strings.LastIndex(key, "#")
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}
