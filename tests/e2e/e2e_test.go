//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	deploymentName = "robodev"
	selectorLabel  = "app.kubernetes.io/name=robodev"
	serviceName    = "robodev-metrics"
	configMapName  = "robodev-config"
	saName         = "robodev"
	crbName        = "robodev"

	readyTimeout = 120 * time.Second
)

func TestControllerDeploymentRunning(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	waitForDeploymentReady(t, client, ns, deploymentName, readyTimeout)

	deploy, err := client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, *deploy.Spec.Replicas, deploy.Status.AvailableReplicas,
		"all replicas should be available")
}

func TestControllerPodReady(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	pod := waitForPodReady(t, client, ns, selectorLabel, readyTimeout)
	assert.Equal(t, corev1.PodRunning, pod.Status.Phase,
		"controller pod should be in Running phase")
}

func TestHealthEndpoints(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	// Ensure the controller is ready before hitting endpoints.
	waitForPodReady(t, client, ns, selectorLabel, readyTimeout)

	endpoint := serviceEndpoint(t, client, ns, serviceName)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	tests := []struct {
		name string
		path string
	}{
		{name: "healthz", path: "/healthz"},
		{name: "readyz", path: "/readyz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := fmt.Sprintf("http://%s%s", endpoint, tc.path)
			resp, err := httpClient.Get(url)
			require.NoError(t, err, "GET %s should succeed", url)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"%s should return 200", tc.path)
		})
	}
}

func TestMetricsEndpoint(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	waitForPodReady(t, client, ns, selectorLabel, readyTimeout)

	endpoint := serviceEndpoint(t, client, ns, serviceName)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	url := fmt.Sprintf("http://%s/metrics", endpoint)
	resp, err := httpClient.Get(url)
	require.NoError(t, err, "GET /metrics should succeed")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "/metrics should return 200")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "go_goroutines",
		"/metrics should contain standard Go metrics")
}

func TestConfigMapCreated(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	cm, err := client.CoreV1().ConfigMaps(ns).Get(
		context.Background(), configMapName, metav1.GetOptions{},
	)
	require.NoError(t, err, "ConfigMap %s should exist", configMapName)
	assert.Contains(t, cm.Data, "config.yaml",
		"ConfigMap should contain config.yaml key")
}

func TestServiceAccountCreated(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	_, err := client.CoreV1().ServiceAccounts(ns).Get(
		context.Background(), saName, metav1.GetOptions{},
	)
	require.NoError(t, err, "ServiceAccount %s should exist", saName)
}

func TestClusterRoleBindingExists(t *testing.T) {
	client := newK8sClient(t)

	_, err := client.RbacV1().ClusterRoleBindings().Get(
		context.Background(), crbName, metav1.GetOptions{},
	)
	require.NoError(t, err, "ClusterRoleBinding %s should exist", crbName)
}

func TestCanCreateJob(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-dryrun-test",
			Namespace: ns,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "test",
							Image:   "busybox",
							Command: []string{"echo", "hello"},
						},
					},
				},
			},
		},
	}

	// Dry-run creation validates RBAC permissions without creating the job.
	_, err := client.BatchV1().Jobs(ns).Create(
		context.Background(), job, metav1.CreateOptions{
			DryRun: []string{metav1.DryRunAll},
		},
	)
	require.NoError(t, err, "dry-run Job creation should succeed (validates batch/v1 RBAC)")
}
