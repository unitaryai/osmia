package jobbuilder

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// PullPolicyFor returns the image pull policy for an agent container.
//
// An image pinned by digest (image@sha256:...) can only ever resolve to one
// image, so a node's cached copy is always correct and PullIfNotPresent is
// safe. Every other reference is a tag, and a tag can be moved: `main` is
// rewritten on every push and by the weekly rebuild. Kubernetes defaults any
// tag other than `latest` to PullIfNotPresent, so a node that pulled an
// earlier `main` keeps running it indefinitely, and which version a job gets
// depends on which node it lands on. That is how a fixed setup-claude.sh
// reached the registry while jobs on one cluster node kept failing with the
// bug it fixed.
//
// PullAlways makes the kubelet check the registry for the tag's current
// digest on every pod start. Layers already on the node are not downloaded
// again, so the cost of a tag that has not moved is one manifest request.
//
// The trade-off: with PullAlways a pod cannot start while the registry is
// unreachable, even if the node already has the image. Deployments that need
// to survive a registry outage, or that run air-gapped, should pin agent
// images by digest, which restores PullIfNotPresent, and pre-pull that digest
// onto every node that can run agent jobs: PullIfNotPresent only avoids the
// registry on a node that already has the image.
func PullPolicyFor(image string) corev1.PullPolicy {
	if strings.Contains(image, "@") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}
