// Package awssm provides a secrets.Backend implementation that reads secrets
// from AWS Secrets Manager using the AWS SDK for Go v2. It supports the
// default credential chain (IRSA on EKS, environment variables, shared
// credentials) and optional cross-account access via STS AssumeRole.
package awssm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	corev1 "k8s.io/api/core/v1"

	"github.com/unitaryai/osmia/pkg/plugin/secrets"
)

const backendName = "aws-secrets-manager"
const defaultCacheTTL = 5 * time.Minute

// Compile-time check that Backend implements secrets.Backend.
var _ secrets.Backend = (*Backend)(nil)

// cacheEntry holds a cached secret value and the time it was fetched.
type cacheEntry struct {
	value     string
	fetchedAt time.Time
}

// Backend implements secrets.Backend for AWS Secrets Manager.
type Backend struct {
	region     string
	assumeRole string
	cacheTTL   time.Duration
	httpClient *http.Client
	logger     *slog.Logger

	// smClient is set either by ensureClient (lazy) or WithSMClient (testing).
	smClient    *secretsmanager.Client
	initMu      sync.Mutex
	initialised bool

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// Option is a functional option for configuring a Backend.
type Option func(*Backend)

// WithRegion sets the AWS region for the Secrets Manager client.
func WithRegion(region string) Option {
	return func(b *Backend) {
		b.region = region
	}
}

// WithAssumeRoleARN sets an IAM role ARN to assume via STS before reading
// secrets. This enables cross-account access for multi-tenant deployments.
func WithAssumeRoleARN(arn string) Option {
	return func(b *Backend) {
		b.assumeRole = arn
	}
}

// WithCacheTTL sets the duration for which secret values are cached in memory.
func WithCacheTTL(ttl time.Duration) Option {
	return func(b *Backend) {
		b.cacheTTL = ttl
	}
}

// WithLogger sets the structured logger.
func WithLogger(logger *slog.Logger) Option {
	return func(b *Backend) {
		b.logger = logger
	}
}

// WithHTTPClient sets a custom HTTP client for the AWS SDK. This is useful
// for testing with httptest.Server.
func WithHTTPClient(client *http.Client) Option {
	return func(b *Backend) {
		b.httpClient = client
	}
}

// WithSMClient sets a pre-configured Secrets Manager client directly,
// bypassing lazy initialisation. This is primarily for testing.
func WithSMClient(client *secretsmanager.Client) Option {
	return func(b *Backend) {
		b.smClient = client
		b.initialised = true
	}
}

// NewBackend creates a new AWS Secrets Manager backend with the given options.
// The AWS SDK client is initialised lazily on the first GetSecret call.
func NewBackend(opts ...Option) *Backend {
	b := &Backend{
		cacheTTL: defaultCacheTTL,
		logger:   slog.Default(),
		cache:    make(map[string]cacheEntry),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// ensureClient initialises the AWS SDK client on first use. Unlike sync.Once,
// this retries on transient failures so that a temporary network issue during
// startup does not permanently disable the backend.
func (b *Backend) ensureClient(ctx context.Context) error {
	b.initMu.Lock()
	defer b.initMu.Unlock()

	if b.initialised {
		return nil
	}

	var cfgOpts []func(*awsconfig.LoadOptions) error

	if b.region != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithRegion(b.region))
	}
	if b.httpClient != nil {
		cfgOpts = append(cfgOpts, awsconfig.WithHTTPClient(b.httpClient))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	// If a cross-account role is configured, wrap credentials with a cached
	// STS AssumeRole provider so temporary credentials are reused across calls.
	if b.assumeRole != "" {
		stsClient := sts.NewFromConfig(cfg)
		cfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(stsClient, b.assumeRole),
		)
		b.logger.Info("configured cross-account role assumption",
			"role_arn", b.assumeRole)
	}

	b.smClient = secretsmanager.NewFromConfig(cfg)
	b.initialised = true
	b.logger.Info("initialised AWS Secrets Manager client",
		"region", cfg.Region)

	return nil
}

// GetSecret retrieves a single secret value from AWS Secrets Manager. The key
// format is "secret-name#json-field" where secret-name is the AWS SM secret
// name or ARN, and json-field optionally selects a field from a JSON-valued
// secret. If no fragment is provided, the raw secret string is returned.
func (b *Backend) GetSecret(ctx context.Context, key string) (string, error) {
	if err := b.ensureClient(ctx); err != nil {
		return "", err
	}

	secretName, jsonField := parseKey(key)

	raw, err := b.fetchOrCache(ctx, secretName)
	if err != nil {
		return "", err
	}

	if jsonField == "" {
		return raw, nil
	}

	return extractJSONField(raw, jsonField, secretName)
}

// GetSecrets retrieves multiple secret values. Keys sharing the same secret
// name result in a single API call (via the cache).
func (b *Backend) GetSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	if err := b.ensureClient(ctx); err != nil {
		return nil, err
	}

	result := make(map[string]string, len(keys))
	for _, key := range keys {
		value, err := b.GetSecret(ctx, key)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

// BuildEnvVars translates secret references into Kubernetes EnvVar
// definitions. Since AWS SM secrets are fetched at resolution time (not
// natively injected via K8s), the values are set directly on the EnvVar.
func (b *Backend) BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error) {
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
func (b *Backend) Name() string {
	return backendName
}

// InterfaceVersion returns the secrets interface version implemented.
func (b *Backend) InterfaceVersion() int {
	return secrets.InterfaceVersion
}

// fetchOrCache retrieves a secret's raw value, using the cache if the entry
// is still within TTL.
func (b *Backend) fetchOrCache(ctx context.Context, secretName string) (string, error) {
	// Check cache with read lock.
	b.mu.RLock()
	entry, cached := b.cache[secretName]
	b.mu.RUnlock()

	if cached && time.Since(entry.fetchedAt) < b.cacheTTL {
		return entry.value, nil
	}

	// Cache miss or expired — fetch from AWS SM.
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock.
	entry, cached = b.cache[secretName]
	if cached && time.Since(entry.fetchedAt) < b.cacheTTL {
		return entry.value, nil
	}

	output, err := b.smClient.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	})
	if err != nil {
		return "", fmt.Errorf("retrieving secret %q from AWS Secrets Manager: %w", secretName, err)
	}

	if output.SecretString == nil {
		return "", fmt.Errorf("secret %q has no string value (binary secrets are not supported)", secretName)
	}

	raw := *output.SecretString
	b.cache[secretName] = cacheEntry{value: raw, fetchedAt: time.Now()}
	b.logger.Info("fetched secret from AWS Secrets Manager", "secret_name", secretName)

	return raw, nil
}

// extractJSONField parses raw as JSON and returns the value of the named field.
func extractJSONField(raw, field, secretName string) (string, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", fmt.Errorf("secret %q is not valid JSON (requested field %q): %w",
			secretName, field, err)
	}

	value, ok := data[field]
	if !ok {
		return "", fmt.Errorf("field %q not found in secret %q", field, secretName)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q in secret %q is not a string", field, secretName)
	}
	return str, nil
}

// parseKey splits a key in "secret-name#json-field" format. If no fragment
// is present, the field is empty.
func parseKey(key string) (secretName, jsonField string) {
	idx := strings.LastIndex(key, "#")
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}
