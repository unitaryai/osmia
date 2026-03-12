//go:build e2e

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestControllerSecurityContext verifies that the controller container is
// configured with a restrictive securityContext as required by the security
// policy: non-root user, read-only filesystem, no privilege escalation, and
// all Linux capabilities dropped.
func TestControllerSecurityContext(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	deploy, err := client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	sc := findControllerSecurityContext(t, deploy.Spec.Template.Spec.Containers)

	assert.True(t, sc.RunAsNonRoot != nil && *sc.RunAsNonRoot,
		"runAsNonRoot should be true")
	assert.True(t, sc.RunAsUser != nil && *sc.RunAsUser == 65534,
		"runAsUser should be 65534 (nobody)")
	assert.True(t, sc.ReadOnlyRootFilesystem != nil && *sc.ReadOnlyRootFilesystem,
		"readOnlyRootFilesystem should be true")
	assert.True(t, sc.AllowPrivilegeEscalation != nil && !*sc.AllowPrivilegeEscalation,
		"allowPrivilegeEscalation should be false")

	require.NotNil(t, sc.Capabilities, "capabilities should be set")
	drops := make([]string, len(sc.Capabilities.Drop))
	for i, c := range sc.Capabilities.Drop {
		drops[i] = string(c)
	}
	assert.Contains(t, drops, "ALL", "all Linux capabilities should be dropped")
}

// TestControllerPodSecurityContext verifies that the controller pod's
// securityContext sets the correct fsGroup and seccomp profile.
func TestControllerPodSecurityContext(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	deploy, err := client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	psc := deploy.Spec.Template.Spec.SecurityContext
	require.NotNil(t, psc, "pod securityContext must be set")

	assert.True(t, psc.FSGroup != nil && *psc.FSGroup == 65534,
		"fsGroup should be 65534")
	require.NotNil(t, psc.SeccompProfile, "seccompProfile must be set")
	assert.Equal(t, "RuntimeDefault", string(psc.SeccompProfile.Type),
		"seccompProfile type should be RuntimeDefault")
}

// TestControllerServiceAccountAutomount verifies that the service account has
// token automounting enabled (nil defaults to true for ServiceAccounts).
func TestControllerServiceAccountAutomount(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	sa, err := client.CoreV1().ServiceAccounts(ns).Get(
		context.Background(), saName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	// A nil value means the Kubernetes default (true); explicit true is also valid.
	automount := sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken
	assert.True(t, automount, "automountServiceAccountToken should be true (or nil)")
}

// TestControllerReadOnlyConfigMount verifies that the /etc/osmia volume
// mount is marked readOnly, preventing accidental mutation of the config.
func TestControllerReadOnlyConfigMount(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	deploy, err := client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	for _, c := range deploy.Spec.Template.Spec.Containers {
		if c.Name != "controller" {
			continue
		}
		for _, vm := range c.VolumeMounts {
			if vm.MountPath == "/etc/osmia" {
				assert.True(t, vm.ReadOnly,
					"/etc/osmia volume mount should be readOnly")
				return
			}
		}
	}
	t.Fatal("could not find /etc/osmia volume mount in controller container")
}

// findControllerSecurityContext locates the "controller" container in the
// given slice and returns its SecurityContext. Fails the test if not found.
func findControllerSecurityContext(t *testing.T, containers []corev1.Container) *corev1.SecurityContext {
	t.Helper()
	for i := range containers {
		if containers[i].Name == "controller" {
			sc := containers[i].SecurityContext
			require.NotNil(t, sc, "controller container must have a securityContext")
			return sc
		}
	}
	t.Fatal("controller container not found in deployment")
	return nil
}
