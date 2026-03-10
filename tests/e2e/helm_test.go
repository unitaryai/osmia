//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelmResourcesExist verifies that all expected Kubernetes resources
// produced by the Helm chart are present in the test namespace.
func TestHelmResourcesExist(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()
	ctx := context.Background()

	t.Run("deployment/osmia", func(t *testing.T) {
		_, err := client.AppsV1().Deployments(ns).Get(ctx, "osmia", metav1.GetOptions{})
		require.NoError(t, err, "Deployment osmia should exist")
	})

	t.Run("service/osmia-metrics", func(t *testing.T) {
		_, err := client.CoreV1().Services(ns).Get(ctx, "osmia-metrics", metav1.GetOptions{})
		require.NoError(t, err, "Service osmia-metrics should exist")
	})

	t.Run("service/osmia-webhook", func(t *testing.T) {
		_, err := client.CoreV1().Services(ns).Get(ctx, "osmia-webhook", metav1.GetOptions{})
		require.NoError(t, err, "Service osmia-webhook should exist")
	})

	t.Run("configmap/osmia-config", func(t *testing.T) {
		_, err := client.CoreV1().ConfigMaps(ns).Get(ctx, "osmia-config", metav1.GetOptions{})
		require.NoError(t, err, "ConfigMap osmia-config should exist")
	})

	t.Run("serviceaccount/osmia", func(t *testing.T) {
		_, err := client.CoreV1().ServiceAccounts(ns).Get(ctx, "osmia", metav1.GetOptions{})
		require.NoError(t, err, "ServiceAccount osmia should exist")
	})

	t.Run("clusterrole/osmia", func(t *testing.T) {
		_, err := client.RbacV1().ClusterRoles().Get(ctx, "osmia", metav1.GetOptions{})
		require.NoError(t, err, "ClusterRole osmia should exist")
	})

	t.Run("clusterrolebinding/osmia", func(t *testing.T) {
		_, err := client.RbacV1().ClusterRoleBindings().Get(ctx, "osmia", metav1.GetOptions{})
		require.NoError(t, err, "ClusterRoleBinding osmia should exist")
	})
}

// TestHelmConfigMapContent verifies that the osmia-config ConfigMap contains
// the expected engine and guardrail configuration.
func TestHelmConfigMapContent(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	cm, err := client.CoreV1().ConfigMaps(ns).Get(
		context.Background(), "osmia-config", metav1.GetOptions{},
	)
	require.NoError(t, err)

	data, ok := cm.Data["config.yaml"]
	require.True(t, ok, "ConfigMap must contain a config.yaml key")

	assert.True(t, strings.Contains(data, "claude-code"),
		"config.yaml should reference the claude-code engine")
	assert.True(t, strings.Contains(data, "guardrails"),
		"config.yaml should contain guardrail settings")
}

// TestHelmClusterRolePermissions verifies that the ClusterRole contains the
// minimum required RBAC rules for the controller to operate.
func TestHelmClusterRolePermissions(t *testing.T) {
	client := newK8sClient(t)

	role, err := client.RbacV1().ClusterRoles().Get(
		context.Background(), "osmia", metav1.GetOptions{},
	)
	require.NoError(t, err)

	type ruleKey struct {
		apiGroup string
		resource string
	}

	// ruleIndex maps (apiGroup, resource) -> set of verbs present in the role.
	ruleIndex := make(map[ruleKey][]string)
	for _, rule := range role.Rules {
		for _, ag := range rule.APIGroups {
			for _, res := range rule.Resources {
				k := ruleKey{apiGroup: ag, resource: res}
				ruleIndex[k] = append(ruleIndex[k], rule.Verbs...)
			}
		}
	}

	tests := []struct {
		apiGroup string
		resource string
		verbs    []string
	}{
		{"batch", "jobs", []string{"create", "delete", "get", "list", "watch"}},
		{"", "pods", []string{"get", "list", "watch"}},
		{"", "secrets", []string{"get"}},
		{"coordination.k8s.io", "leases", []string{"get", "list", "watch"}},
	}

	for _, tc := range tests {
		k := ruleKey{apiGroup: tc.apiGroup, resource: tc.resource}
		presentVerbs := ruleIndex[k]
		assert.NotEmpty(t, presentVerbs,
			"ClusterRole should have a rule for %s/%s", tc.apiGroup, tc.resource)
		for _, v := range tc.verbs {
			assert.Contains(t, presentVerbs, v,
				"ClusterRole rule for %s/%s should include verb %q", tc.apiGroup, tc.resource, v)
		}
	}
}

// TestHelmWebhookServicePort verifies that the webhook Service targets port 8081.
func TestHelmWebhookServicePort(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	svc, err := client.CoreV1().Services(ns).Get(
		context.Background(), "osmia-webhook", metav1.GetOptions{},
	)
	require.NoError(t, err)

	for _, p := range svc.Spec.Ports {
		if p.Port == 8081 {
			return
		}
	}
	t.Fatalf("webhook Service does not expose port 8081; ports: %v", svc.Spec.Ports)
}
