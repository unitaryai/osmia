//go:build e2e

package e2e

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkPolicyControllerExists verifies that the controller NetworkPolicy
// has been deployed in the test namespace.
func TestNetworkPolicyControllerExists(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	_, err := client.NetworkingV1().NetworkPolicies(ns).Get(
		context.Background(), "osmia-controller", metav1.GetOptions{},
	)
	require.NoError(t, err, "NetworkPolicy osmia-controller should exist")
}

// TestNetworkPolicyControllerRules verifies that the controller NetworkPolicy
// allows ingress on ports 8080 (metrics) and 8081 (webhook), and egress on
// port 53 (DNS), 443 (HTTPS), and 6443 (Kubernetes API server).
func TestNetworkPolicyControllerRules(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	netpol, err := client.NetworkingV1().NetworkPolicies(ns).Get(
		context.Background(), "osmia-controller", metav1.GetOptions{},
	)
	require.NoError(t, err)

	// Collect all ingress ports.
	var ingressPorts []int32
	for _, rule := range netpol.Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port != nil {
				ingressPorts = append(ingressPorts, p.Port.IntVal)
			}
		}
	}

	// Collect all egress ports.
	var egressPorts []int32
	for _, rule := range netpol.Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil {
				egressPorts = append(egressPorts, p.Port.IntVal)
			}
		}
	}

	assert.Contains(t, ingressPorts, int32(8080),
		"controller NetworkPolicy should allow ingress on port 8080 (metrics)")
	assert.Contains(t, ingressPorts, int32(8081),
		"controller NetworkPolicy should allow ingress on port 8081 (webhook)")

	assert.Contains(t, egressPorts, int32(53),
		"controller NetworkPolicy should allow egress on port 53 (DNS)")
	assert.Contains(t, egressPorts, int32(443),
		"controller NetworkPolicy should allow egress on port 443 (HTTPS)")
	assert.Contains(t, egressPorts, int32(6443),
		"controller NetworkPolicy should allow egress on port 6443 (Kubernetes API server)")
}

// TestNetworkPolicyAgentExists verifies that the agent NetworkPolicy has been
// deployed in the test namespace.
func TestNetworkPolicyAgentExists(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	_, err := client.NetworkingV1().NetworkPolicies(ns).Get(
		context.Background(), "osmia-agent", metav1.GetOptions{},
	)
	require.NoError(t, err, "NetworkPolicy osmia-agent should exist")
}

// TestNetworkPolicyAgentRules verifies that the agent NetworkPolicy denies
// all inbound traffic (empty ingress) and allows egress only on ports 53
// (DNS), 443 (HTTPS), and 22 (SSH for git operations).
func TestNetworkPolicyAgentRules(t *testing.T) {
	client := newK8sClient(t)
	ns := testNamespace()

	netpol, err := client.NetworkingV1().NetworkPolicies(ns).Get(
		context.Background(), "osmia-agent", metav1.GetOptions{},
	)
	require.NoError(t, err)

	assert.Empty(t, netpol.Spec.Ingress,
		"agent NetworkPolicy should have empty ingress (deny all inbound)")

	// Collect all egress ports.
	var egressPorts []int32
	for _, rule := range netpol.Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil {
				egressPorts = append(egressPorts, p.Port.IntVal)
			}
		}
	}

	assert.Contains(t, egressPorts, int32(53),
		"agent NetworkPolicy should allow egress on port 53 (DNS)")
	assert.Contains(t, egressPorts, int32(443),
		"agent NetworkPolicy should allow egress on port 443 (HTTPS)")
	assert.Contains(t, egressPorts, int32(22),
		"agent NetworkPolicy should allow egress on port 22 (SSH)")
}
