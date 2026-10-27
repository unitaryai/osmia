package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

const digest = "sha256:896e95151a23c6cf2afe691e13aa3c477aaa7010345399a529b14aa216e2a56a"

func TestPullPolicyFor(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  corev1.PullPolicy
	}{
		// The case that broke a real deployment: `main` is rewritten on every push and
		// by the weekly rebuild, so a cached copy can be any earlier build.
		{name: "moving branch tag", image: "ghcr.io/unitaryai/osmia/engine-claude-code:main", want: corev1.PullAlways},
		{name: "commit tag is still a tag", image: "ghcr.io/unitaryai/osmia/engine-claude-code:sha-e037eae", want: corev1.PullAlways},
		{name: "release tag is still a tag", image: "ghcr.io/unitaryai/osmia/engine-claude-code:0.5.1", want: corev1.PullAlways},
		{name: "no tag", image: "ghcr.io/unitaryai/osmia/engine-claude-code", want: corev1.PullAlways},
		{name: "registry port is not mistaken for a digest", image: "localhost:5000/engine:main", want: corev1.PullAlways},
		{name: "pinned by digest", image: "ghcr.io/unitaryai/osmia/engine-claude-code@" + digest, want: corev1.PullIfNotPresent},
		{name: "tag plus digest resolves by digest", image: "ghcr.io/unitaryai/osmia/engine-claude-code:main@" + digest, want: corev1.PullIfNotPresent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PullPolicyFor(tt.image))
		})
	}
}

// TestBuild_SetsPullPolicy pins that the policy actually reaches the agent
// container. Without an explicit value Kubernetes defaults a `main` tag to
// IfNotPresent, which is the bug this exists to prevent.
func TestBuild_SetsPullPolicy(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  corev1.PullPolicy
	}{
		{name: "tagged image is always re-checked", image: "ghcr.io/unitaryai/osmia/engine-claude-code:main", want: corev1.PullAlways},
		{name: "digest-pinned image uses the node cache", image: "ghcr.io/unitaryai/osmia/engine-claude-code@" + digest, want: corev1.PullIfNotPresent},
	}

	for _, tt := range tests {
		t.Run("job/"+tt.name, func(t *testing.T) {
			spec := validSpec()
			spec.Image = tt.image

			job, err := NewJobBuilder("default").Build("tr-pull", "claude-code", spec)
			require.NoError(t, err)

			assert.Equal(t, tt.want, job.Spec.Template.Spec.Containers[0].ImagePullPolicy)
		})

		t.Run("docker/"+tt.name, func(t *testing.T) {
			spec := validSpec()
			spec.Image = tt.image

			job, err := NewDockerBuilder("default").Build("tr-pull", "claude-code", spec)
			require.NoError(t, err)

			assert.Equal(t, tt.want, job.Spec.Template.Spec.Containers[0].ImagePullPolicy)
		})
	}
}
