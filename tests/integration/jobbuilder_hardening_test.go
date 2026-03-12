//go:build integration

package integration_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/aider"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/engine/cline"
	"github.com/unitaryai/osmia/pkg/engine/codex"
	"github.com/unitaryai/osmia/pkg/engine/opencode"
)

// allEngines returns one instance of every supported execution engine.
func allEngines() []engine.ExecutionEngine {
	return []engine.ExecutionEngine{
		claudecode.New(),
		codex.New(),
		aider.New(),
		opencode.New(),
		cline.New(),
	}
}

// TestJobBuilderSecurityHardening verifies that each engine produces a Job
// with a fully restrictive container security context and a pod spec that
// disables retries.
func TestJobBuilderSecurityHardening(t *testing.T) {
	t.Parallel()

	builder := jobbuilder.NewJobBuilder("test-ns")

	for _, eng := range allEngines() {
		eng := eng // capture range variable
		t.Run(eng.Name(), func(t *testing.T) {
			t.Parallel()

			spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
			require.NoError(t, err)

			job, err := builder.Build("run-abc", eng.Name(), spec)
			require.NoError(t, err)
			require.NotNil(t, job)

			podSpec := job.Spec.Template.Spec
			require.Len(t, podSpec.Containers, 1, "expected exactly one container")

			sc := podSpec.Containers[0].SecurityContext
			require.NotNil(t, sc, "container security context must not be nil")

			// runAsNonRoot must be true.
			require.NotNil(t, sc.RunAsNonRoot, "RunAsNonRoot must be set")
			assert.True(t, *sc.RunAsNonRoot, "RunAsNonRoot must be true")

			// runAsUser must be 1000.
			require.NotNil(t, sc.RunAsUser, "RunAsUser must be set")
			assert.Equal(t, int64(1000), *sc.RunAsUser, "RunAsUser must be 1000")

			// readOnlyRootFilesystem must be true.
			require.NotNil(t, sc.ReadOnlyRootFilesystem, "ReadOnlyRootFilesystem must be set")
			assert.True(t, *sc.ReadOnlyRootFilesystem, "ReadOnlyRootFilesystem must be true")

			// allowPrivilegeEscalation must be false.
			require.NotNil(t, sc.AllowPrivilegeEscalation, "AllowPrivilegeEscalation must be set")
			assert.False(t, *sc.AllowPrivilegeEscalation, "AllowPrivilegeEscalation must be false")

			// ALL capabilities must be dropped.
			require.NotNil(t, sc.Capabilities, "Capabilities must be set")
			assert.Contains(t, sc.Capabilities.Drop, corev1.Capability("ALL"),
				"must drop ALL capabilities")

			// Seccomp profile must be RuntimeDefault.
			require.NotNil(t, sc.SeccompProfile, "SeccompProfile must be set")
			assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type,
				"seccomp profile must be RuntimeDefault")

			// RestartPolicy must be Never.
			assert.Equal(t, corev1.RestartPolicyNever, podSpec.RestartPolicy,
				"RestartPolicy must be Never")

			// BackoffLimit must be 0.
			require.NotNil(t, job.Spec.BackoffLimit, "BackoffLimit must be set")
			assert.Equal(t, int32(0), *job.Spec.BackoffLimit, "BackoffLimit must be 0")
		})
	}
}

// TestJobBuilderLabelsAndTolerations verifies that the built Job carries the
// required osmia labels and the agent node toleration.
func TestJobBuilderLabelsAndTolerations(t *testing.T) {
	t.Parallel()

	eng := claudecode.New()
	spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
	require.NoError(t, err)

	builder := jobbuilder.NewJobBuilder("test-ns")
	job, err := builder.Build("run-labels-test", eng.Name(), spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	labels := job.Labels
	assert.Equal(t, "osmia-agent", labels["app"],
		`label "app" must equal "osmia-agent"`)
	assert.NotEmpty(t, labels["osmia.io/task-run-id"],
		`label "osmia.io/task-run-id" must not be empty`)
	assert.NotEmpty(t, labels["osmia.io/engine"],
		`label "osmia.io/engine" must not be empty`)

	// Verify the toleration for osmia.io/agent is present with correct settings.
	tolerations := job.Spec.Template.Spec.Tolerations
	hasToleration := false
	for _, tol := range tolerations {
		if tol.Key == "osmia.io/agent" {
			hasToleration = true
			assert.Equal(t, corev1.TolerationOpExists, tol.Operator,
				"toleration operator must be Exists")
			assert.Equal(t, corev1.TaintEffectNoSchedule, tol.Effect,
				"toleration effect must be NoSchedule")
			break
		}
	}
	assert.True(t, hasToleration, `toleration for "osmia.io/agent" must be present`)
}

// TestJobBuilderNameLength verifies that even when a very long task run ID is
// provided, the resulting Kubernetes Job name does not exceed 63 characters.
func TestJobBuilderNameLength(t *testing.T) {
	t.Parallel()

	// Construct a task run ID that is well over the Kubernetes 63-char limit.
	longTaskRunID := strings.Repeat("x", 100)

	eng := claudecode.New()
	spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
	require.NoError(t, err)

	builder := jobbuilder.NewJobBuilder("test-ns")
	job, err := builder.Build(longTaskRunID, eng.Name(), spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	assert.LessOrEqual(t, len(job.Name), 63,
		"Job name must be at most 63 characters; got %d: %q", len(job.Name), job.Name)
}
