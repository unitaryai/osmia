package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/unitaryai/osmia/pkg/engine"
)

func TestDockerBuild(t *testing.T) {
	tests := []struct {
		name       string
		taskRunID  string
		engineName string
		spec       *engine.ExecutionSpec
		wantErr    bool
		errContain string
	}{
		{
			name:       "valid spec produces correct docker job",
			taskRunID:  "tr-123",
			engineName: "claude-code",
			spec:       validSpec(),
		},
		{
			name:       "missing image returns error",
			taskRunID:  "tr-123",
			engineName: "claude-code",
			spec: &engine.ExecutionSpec{
				Command: []string{"echo", "hello"},
			},
			wantErr:    true,
			errContain: "missing required image",
		},
		{
			name:       "empty command returns error",
			taskRunID:  "tr-123",
			engineName: "claude-code",
			spec: &engine.ExecutionSpec{
				Image: "ghcr.io/osmia/agent:latest",
			},
			wantErr:    true,
			errContain: "missing required command",
		},
		{
			name:       "minimal spec with no optional fields",
			taskRunID:  "tr-minimal",
			engineName: "codex",
			spec: &engine.ExecutionSpec{
				Image:   "ghcr.io/osmia/codex:latest",
				Command: []string{"codex", "run"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewDockerBuilder("default")
			job, err := builder.Build(tt.taskRunID, tt.engineName, tt.spec)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
				assert.Nil(t, job)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, job)
		})
	}
}

func TestDockerBuild_BackendAnnotation(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-ann", "claude-code", validSpec())
	require.NoError(t, err)

	// Job-level annotation.
	assert.Equal(t, "local", job.Annotations[annotationBackend])

	// Pod template annotation.
	assert.Equal(t, "local", job.Spec.Template.Annotations[annotationBackend])
}

func TestDockerBuild_Labels(t *testing.T) {
	builder := NewDockerBuilder("agents")
	job, err := builder.Build("tr-456", "claude-code", validSpec())
	require.NoError(t, err)

	// Job-level labels.
	assert.Equal(t, "osmia-agent", job.Labels[labelApp])
	assert.Equal(t, "tr-456", job.Labels[LabelTaskRunID])
	assert.Equal(t, "claude-code", job.Labels[labelEngine])

	// Pod template labels.
	podLabels := job.Spec.Template.Labels
	assert.Equal(t, "osmia-agent", podLabels[labelApp])
	assert.Equal(t, "tr-456", podLabels[LabelTaskRunID])
	assert.Equal(t, "claude-code", podLabels[labelEngine])
}

func TestDockerBuild_SecurityContext(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-sec", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	sc := container.SecurityContext
	require.NotNil(t, sc)

	assert.True(t, *sc.RunAsNonRoot, "runAsNonRoot should be true")
	assert.Equal(t, int64(10000), *sc.RunAsUser, "runAsUser should match Dockerfile UID")
	assert.True(t, *sc.ReadOnlyRootFilesystem, "readOnlyRootFilesystem should be true")
	assert.False(t, *sc.AllowPrivilegeEscalation, "allowPrivilegeEscalation should be false")

	require.NotNil(t, sc.Capabilities)
	assert.Contains(t, sc.Capabilities.Drop, corev1.Capability("ALL"))

	require.NotNil(t, sc.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)
}

func TestDockerBuild_Volumes(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-vol", "claude-code", validSpec())
	require.NoError(t, err)

	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, "workspace", job.Spec.Template.Spec.Volumes[0].Name)

	container := job.Spec.Template.Spec.Containers[0]
	require.Len(t, container.VolumeMounts, 1)
	assert.Equal(t, "workspace", container.VolumeMounts[0].Name)
	assert.Equal(t, "/workspace", container.VolumeMounts[0].MountPath)
}

func TestDockerBuild_EnvVars(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-env", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	require.NotEmpty(t, container.Env)

	found := false
	for _, e := range container.Env {
		if e.Name == "REPO_URL" {
			assert.Equal(t, "https://github.com/example/repo", e.Value)
			found = true
		}
	}
	assert.True(t, found, "REPO_URL env var should be present")
}

func TestDockerBuild_SecretEnv(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-secret", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	require.NotEmpty(t, container.EnvFrom)

	found := false
	for _, ef := range container.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "agent-secrets" {
			found = true
		}
	}
	assert.True(t, found, "agent-secrets envFrom should be present")
}

func TestDockerBuild_Resources(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-res", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	reqs := container.Resources

	assert.Equal(t, resource.MustParse("500m"), reqs.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), reqs.Requests[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("2"), reqs.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("4Gi"), reqs.Limits[corev1.ResourceMemory])
}

func TestDockerBuild_Namespace(t *testing.T) {
	builder := NewDockerBuilder("osmia-agents")
	job, err := builder.Build("tr-ns", "codex", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "osmia-agents", job.Namespace)
}

func TestDockerBuild_JobName(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-123", "claude-code", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "osmia-tr-123", job.Name)
}

func TestDockerBuild_LongTaskRunID_Truncated(t *testing.T) {
	longID := "this-is-a-very-long-task-run-id-that-exceeds-sixty-three-characters-limit"
	builder := NewDockerBuilder("default")
	job, err := builder.Build(longID, "claude-code", validSpec())
	require.NoError(t, err)

	assert.LessOrEqual(t, len(job.Name), 63, "job name must not exceed 63 characters")
}

func TestDockerBuild_ActiveDeadline(t *testing.T) {
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-dl", "claude-code", validSpec())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int64(3600), *job.Spec.ActiveDeadlineSeconds)
}

func TestDockerBuild_ActiveDeadline_ZeroOmitted(t *testing.T) {
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
	}
	builder := NewDockerBuilder("default")
	job, err := builder.Build("tr-no-dl", "claude-code", spec)
	require.NoError(t, err)

	assert.Nil(t, job.Spec.ActiveDeadlineSeconds)
}
