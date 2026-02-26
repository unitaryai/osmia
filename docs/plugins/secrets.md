# Secrets Backend Interface

## Overview

The secrets backend retrieves sensitive values (API keys, tokens, credentials) from external secret stores and builds Kubernetes environment variable references for injection into agent job pods. It abstracts the underlying secrets store so that the controller and engines can request secrets without knowing whether they come from Kubernetes Secrets, HashiCorp Vault, AWS Secrets Manager, or another provider.

## Interface Summary

| Property | Value |
|---|---|
| Proto definition | `proto/secrets.proto` |
| Go interface | `pkg/plugin/secrets/secrets.go` |
| Interface version | `1` |
| Role in lifecycle | Called during job creation to inject secrets into agent pod environment |
| Criticality | Critical — if the secrets backend is unavailable, jobs cannot be created |

## Go Interface

```go
type Backend interface {
    // GetSecret retrieves a single secret value by key.
    GetSecret(ctx context.Context, key string) (string, error)

    // GetSecrets retrieves multiple secrets in a single call.
    GetSecrets(ctx context.Context, keys []string) (map[string]string, error)

    // BuildEnvVars translates secret references into Kubernetes EnvVar specs.
    // Keys in the map are environment variable names, values are secret keys.
    BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error)

    // Name returns the unique identifier for this backend.
    Name() string

    // InterfaceVersion returns the interface version.
    InterfaceVersion() int
}
```

## RPC Methods

### Handshake

Version negotiation called once at plugin startup.

```protobuf
rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
```

### GetSecret

Retrieves a single secret value by key.

```protobuf
rpc GetSecret(GetSecretRequest) returns (GetSecretResponse);

message GetSecretRequest {
  string key = 1;           // The secret key to retrieve.
}

message GetSecretResponse {
  string value = 1;         // The secret value.
}
```

**Implementation guidance:**

- Return a gRPC `NOT_FOUND` error if the key does not exist.
- **Never log the secret value** — only log the key name.
- Values are typically short-lived (API keys, tokens). Consider caching with a short TTL (30–60 seconds) if the backing store is slow.
- Respect the `context.Context` deadline — secret retrieval should complete within 5 seconds.

### GetSecrets

Retrieves multiple secrets in a single call. More efficient than calling `GetSecret` repeatedly.

```protobuf
rpc GetSecrets(GetSecretsRequest) returns (GetSecretsResponse);

message GetSecretsRequest {
  repeated string keys = 1;
}

message GetSecretsResponse {
  map<string, string> secrets = 1;  // Key-value pairs. Missing keys are omitted.
}
```

**Implementation guidance:**

- Omit keys from the response map that were not found, rather than returning errors for individual missing keys.
- If any keys are critical and missing, the caller will detect the omission and handle it.
- Batch requests to the backing store where possible (e.g., Vault's batch secret reads).

### BuildEnvVars

Translates secret references into Kubernetes `EnvVar` specs. This is the preferred method for injecting secrets into agent pods because it supports Kubernetes-native `SecretKeyRef` references.

```protobuf
rpc BuildEnvVars(BuildEnvVarsRequest) returns (BuildEnvVarsResponse);

message BuildEnvVarsRequest {
  repeated SecretRef secret_refs = 1;
}

message SecretRef {
  string env_name = 1;      // The environment variable name in the pod.
  string secret_key = 2;    // The key in the secrets store.
}

message BuildEnvVarsResponse {
  repeated EnvVar env_vars = 1;
}

message EnvVar {
  string name = 1;
  string value = 2;                    // Direct value (mutually exclusive with value_from).
  SecretKeyRef value_from = 3;         // Reference to a K8s Secret (preferred).
}

message SecretKeyRef {
  string name = 1;                     // The Kubernetes Secret name.
  string key = 2;                      // The key within the Secret.
}
```

**Implementation guidance:**

- **Prefer `SecretKeyRef` over direct values.** When returning `SecretKeyRef` references, secrets are injected by the kubelet without passing through the controller process. This is more secure and avoids having plaintext secrets in memory.
- For external backends (Vault, AWS SM), consider creating ephemeral Kubernetes Secrets and returning references to them, rather than passing plaintext values through the controller.
- The `value` and `value_from` fields are mutually exclusive — set one or the other, not both.

## Built-in: Kubernetes Secrets

The default backend reads secrets directly from Kubernetes Secret objects in the controller's namespace.

### Configuration

```yaml
config:
  secrets:
    backend: k8s
    config:
      namespace: "robodev"    # Optional — defaults to the controller's namespace.
```

No additional configuration is required. The backend uses the controller's service account credentials to read Secrets.

### Key Format

Secret keys use the format `secretName/key`:

- `robodev-anthropic-key/api_key` reads the `api_key` data key from the `robodev-anthropic-key` Secret.
- `github-token/token` reads the `token` data key from the `github-token` Secret.

### Behaviour

| Method | Kubernetes Action |
|---|---|
| `GetSecret` | Reads the named Secret and returns the value for the specified key |
| `GetSecrets` | Reads multiple Secrets and returns all requested key-value pairs |
| `BuildEnvVars` | Returns `SecretKeyRef` entries pointing directly to K8s Secrets |

### Required RBAC

The controller's service account needs read access to Secrets in its namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: robodev-secrets-reader
  namespace: robodev
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list"]
```

This Role is included in the Helm chart by default.

## Other Backends

Third-party plugins are available for external secret stores:

| Backend | Description | Authentication |
|---|---|---|
| `vault` | HashiCorp Vault via API or Agent sidecar | Vault token, K8s auth, AppRole |
| `aws-sm` | AWS Secrets Manager | IRSA (IAM Roles for Service Accounts) |
| `1password` | 1Password Connect server or CLI | Connect token |
| `external-secrets` | Kubernetes External Secrets Operator | Delegated to ESO |
| `azure-kv` | Azure Key Vault | Workload identity federation |

These backends are deployed as third-party gRPC plugins. See [Writing a Plugin](writing-a-plugin.md) for a TypeScript example implementing the `SecretsBackend` interface.

## Security Considerations

Secrets handling is the most security-sensitive area of RoboDev. Follow these principles:

### Never Log Secret Values

The controller and all plugins must never log secret values. Only log key names and metadata:

```go
// Correct
logger.Info("retrieved secret", "key", key)

// NEVER do this
logger.Info("retrieved secret", "key", key, "value", value)
```

### Prefer Kubernetes-Native References

When possible, use `SecretKeyRef` in `BuildEnvVars` rather than direct values. This ensures:

- Secrets are injected by the kubelet, never transiting the controller process.
- Secrets are visible in the pod spec only as references, not plaintext.
- Kubernetes RBAC controls who can read the underlying Secret objects.

### Scope Secrets Narrowly

Each agent job should only have access to the secrets it needs:

- API key for the chosen engine (e.g., `anthropic-api-key` for Claude Code).
- Repository access token (e.g., `github-token`).
- Any task-specific credentials declared in the ticket metadata.

Do not mount the controller's full secret set into agent pods.

### Rotate Credentials Regularly

Use short-lived tokens where possible:

| Credential Type | Rotation Strategy |
|---|---|
| GitHub App installation tokens | Automatic 1-hour expiry |
| Vault dynamic secrets | TTL-based automatic rotation |
| AWS STS temporary credentials | Via IRSA with automatic refresh |
| Static API keys (Anthropic, OpenAI) | Manual rotation on a schedule |

### Audit Secret Access

Log every secret access (key name only) for audit trails. This allows security teams to:

- Track which secrets are accessed and by which components.
- Detect unusual access patterns (e.g., a secret being read far more often than expected).
- Investigate incidents by correlating secret access with job creation events.

## Protobuf Definition

The complete protobuf service is defined in `proto/secrets.proto`:

```protobuf
service SecretsBackend {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc GetSecret(GetSecretRequest) returns (GetSecretResponse);
    rpc GetSecrets(GetSecretsRequest) returns (GetSecretsResponse);
    rpc BuildEnvVars(BuildEnvVarsRequest) returns (BuildEnvVarsResponse);
}
```

See `proto/common.proto` for the shared `HandshakeRequest`/`HandshakeResponse` message definitions.
