#!/usr/bin/env bash
# setup-irsa.sh — Create an IAM role for the Osmia controller with IRSA.
#
# This script creates:
#   1. An IAM policy granting secretsmanager:GetSecretValue
#   2. An IAM role with a trust policy for the EKS OIDC provider
#   3. Annotates the Osmia ServiceAccount (if it exists)
#
# Prerequisites:
#   - AWS CLI v2 configured with appropriate permissions
#   - eksctl installed (used for IRSA association)
#   - An EKS cluster with an OIDC provider enabled
#
# Usage:
#   export CLUSTER_NAME=my-cluster
#   export AWS_REGION=eu-west-1
#   export AWS_ACCOUNT_ID=123456789012
#   export NAMESPACE=osmia-system
#   ./setup-irsa.sh

set -euo pipefail

: "${CLUSTER_NAME:?Set CLUSTER_NAME}"
: "${AWS_REGION:?Set AWS_REGION}"
: "${AWS_ACCOUNT_ID:?Set AWS_ACCOUNT_ID}"
: "${NAMESPACE:=osmia-system}"

ROLE_NAME="osmia-controller-${CLUSTER_NAME}"
POLICY_NAME="osmia-secrets-reader-${CLUSTER_NAME}"
SA_NAME="osmia"

echo "==> Creating IAM policy: ${POLICY_NAME}"
POLICY_ARN=$(aws iam create-policy \
  --policy-name "${POLICY_NAME}" \
  --policy-document '{
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": ["secretsmanager:GetSecretValue"],
        "Resource": "arn:aws:secretsmanager:'"${AWS_REGION}"':'"${AWS_ACCOUNT_ID}"':secret:osmia/*"
      }
    ]
  }' \
  --query 'Policy.Arn' --output text 2>/dev/null || \
  aws iam list-policies \
    --query "Policies[?PolicyName=='${POLICY_NAME}'].Arn" \
    --output text)

echo "    Policy ARN: ${POLICY_ARN}"

echo "==> Creating IRSA association: ${ROLE_NAME}"
eksctl create iamserviceaccount \
  --cluster="${CLUSTER_NAME}" \
  --namespace="${NAMESPACE}" \
  --name="${SA_NAME}" \
  --role-name="${ROLE_NAME}" \
  --attach-policy-arn="${POLICY_ARN}" \
  --approve \
  --override-existing-serviceaccounts

ROLE_ARN="arn:aws:iam::${AWS_ACCOUNT_ID}:role/${ROLE_NAME}"
echo ""
echo "==> Done. Add this to your values-eks.yaml:"
echo ""
echo "serviceAccount:"
echo "  create: true"
echo "  annotations:"
echo "    eks.amazonaws.com/role-arn: \"${ROLE_ARN}\""
echo ""
echo "Or pass it as a Helm set:"
echo "  --set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=${ROLE_ARN}"
