//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// testNamespace returns the namespace used for e2e tests.
// It reads from the ROBODEV_E2E_NAMESPACE environment variable,
// defaulting to "robodev".
func testNamespace() string {
	if ns := os.Getenv("ROBODEV_E2E_NAMESPACE"); ns != "" {
		return ns
	}
	return "robodev"
}

// newK8sClient creates a real Kubernetes client from the current KUBECONFIG.
func newK8sClient(t *testing.T) kubernetes.Interface {
	t.Helper()

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		t.Fatalf("failed to load kubeconfig: %v", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	return client
}

// waitForPodReady polls until at least one pod matching the given label
// selector is in the Ready condition, or the timeout is reached.
func waitForPodReady(t *testing.T, client kubernetes.Interface, ns, labelSelector string, timeout time.Duration) *corev1.Pod {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := client.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			t.Logf("error listing pods: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for i := range pods.Items {
			pod := &pods.Items[i]
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return pod
				}
			}
		}

		time.Sleep(2 * time.Second)
	}

	t.Fatalf("timed out waiting for pod with selector %q in namespace %q to become ready", labelSelector, ns)
	return nil
}

// waitForDeploymentReady polls until the named deployment has all replicas
// available, or the timeout is reached.
func waitForDeploymentReady(t *testing.T, client kubernetes.Interface, ns, name string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		deploy, err := client.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Logf("error getting deployment %s: %v", name, err)
			time.Sleep(2 * time.Second)
			continue
		}

		if deploy.Status.AvailableReplicas == *deploy.Spec.Replicas {
			return
		}

		t.Logf("deployment %s: %d/%d replicas available",
			name, deploy.Status.AvailableReplicas, *deploy.Spec.Replicas)
		time.Sleep(2 * time.Second)
	}

	t.Fatalf("timed out waiting for deployment %q in namespace %q to become ready", name, ns)
}

// serviceEndpoint returns the host:port for accessing a NodePort service from
// the host machine. For kind clusters, this is localhost:<nodePort>.
func serviceEndpoint(t *testing.T, client kubernetes.Interface, ns, name string) string {
	t.Helper()

	svc, err := client.CoreV1().Services(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get service %s/%s: %v", ns, name, err)
	}

	for _, port := range svc.Spec.Ports {
		if port.NodePort != 0 {
			return fmt.Sprintf("localhost:%d", port.NodePort)
		}
	}

	t.Fatalf("service %s/%s has no NodePort", ns, name)
	return ""
}
