# AWS / EKS Deployment Examples

This directory contains EKS-specific configuration for deploying Osmia on Amazon EKS.

## Files

| File | Description |
|------|-------------|
| `values-eks.yaml` | Helm values overlay for EKS — IRSA annotations, ALB ingress, AWS Secrets Manager backend, gp3 session persistence, NetworkPolicy |
| `external-secret.yaml` | ExternalSecret manifests for teams using External Secrets Operator instead of the native AWS SM backend |
| `setup-irsa.sh` | Script to create the IAM role and IRSA association for the Osmia controller |

Also see:

| File | Description |
|------|-------------|
| `../karpenter/nodepool.yaml` | Karpenter NodePool and EC2NodeClass for agent pods (On-Demand, gp3 EBS, taints) |

## Quick Start

```bash
# 1. Set up IRSA (creates IAM role + policy + ServiceAccount annotation)
export CLUSTER_NAME=my-cluster
export AWS_REGION=eu-west-1
export AWS_ACCOUNT_ID=123456789012
./setup-irsa.sh

# 2. Create K8s secrets for API keys
kubectl create namespace osmia-system
kubectl -n osmia-system create secret generic osmia-anthropic \
  --from-literal=api_key="sk-ant-..."
kubectl -n osmia-system create secret generic osmia-github-token \
  --from-literal=token="ghp_..."

# 3. Apply the Karpenter NodePool for agent pods
envsubst < ../karpenter/nodepool.yaml | kubectl apply -f -

# 4. Install Osmia with EKS-specific values
helm repo add osmia https://unitaryai.github.io/osmia
helm install osmia osmia/osmia \
  --namespace osmia-system \
  -f values-eks.yaml
```

## Secrets Management Options

**Option A — Native AWS Secrets Manager (recommended)**

The `values-eks.yaml` file configures the built-in `aws-sm://` backend. Secrets are read directly from AWS Secrets Manager using the IRSA credentials on the controller pod. No additional operators needed.

**Option B — External Secrets Operator**

If your team already runs [ESO](https://external-secrets.io/), apply the manifests in `external-secret.yaml` to sync secrets into K8s. Then use only the `k8s` backend in the Osmia config. See the commented-out section in `values-eks.yaml`.

Note: `setup-irsa.sh` only creates the IRSA role for the Osmia controller ServiceAccount (`osmia` in `osmia-system`). If you choose Option B, you must also configure IRSA for the `external-secrets-sa` ServiceAccount in the `external-secrets` namespace — this is the ServiceAccount referenced in the `ClusterSecretStore` in `external-secret.yaml`. Create an IAM role with the same `secretsmanager:GetSecretValue` policy and associate it using `eksctl create iamserviceaccount` or your preferred IRSA tooling. See the [ESO AWS authentication docs](https://external-secrets.io/latest/provider/aws-secrets-manager/) for full setup instructions.

Both options use IRSA for authentication — no static AWS credentials are involved.

## Session Persistence on EKS

Agent session persistence uses PVCs. On EKS:

- **`per-taskrun-pvc`** (default in `values-eks.yaml`) — dynamically provisions a gp3 EBS volume per task run. Stronger isolation. Requires a gp3 StorageClass.
- **`shared-pvc`** — a single EFS-backed ReadWriteMany PVC shared across tasks. Simpler to operate but less isolated.

## Karpenter

The NodePool in `../karpenter/nodepool.yaml` is configured for **On-Demand** instances. Spot is not recommended for agent workloads because reclamation loses all token spend and progress. See the main documentation for details.
