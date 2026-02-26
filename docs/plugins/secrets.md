# Secrets Backend Interface

## Overview

The secrets backend retrieves sensitive values (API keys, tokens, credentials) from external secret stores and builds Kubernetes environment variable references for injection into agent job pods.

## Interface

```go
type Backend interface {
    GetSecret(ctx context.Context, key string) (string, error)
    GetSecrets(ctx context.Context, keys []string) (map[string]string, error)
    BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error)
    Name() string
    InterfaceVersion() int
}
```

## Built-in: Kubernetes Secrets

The default backend reads secrets directly from Kubernetes Secret objects.

### Configuration

```yaml
secrets:
  backend: k8s
  config:
    namespace: "robodev"
```

### Key Format

Secret keys use the format `secretName/key`:
- `robodev-anthropic-key/api-key` reads the `api-key` data key from the `robodev-anthropic-key` Secret
- `BuildEnvVars` generates `SecretKeyRef` entries pointing to the K8s Secret

## Other Backends

| Backend | Description |
|---------|-------------|
| `vault` | HashiCorp Vault via Vault Agent sidecar or CSI driver |
| `aws-sm` | AWS Secrets Manager via IRSA |
| `1password` | 1Password Connect server or CLI |
| `external-secrets` | Kubernetes External Secrets Operator |

These are available as third-party plugins.

## Protobuf Definition

See `proto/secrets.proto` for the full service definition.
