package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/unitaryai/robodev/pkg/engine"
)

func validSpec() *engine.ExecutionSpec {
	return &engine.ExecutionSpec{
		Image:   "ghcr.io/robodev/agent:latest",
		Command: []string{"claude", "--task", "fix bug"},
		Env: map[string]string{
			"REPO_URL": "https://github.com/example/repo",
		},
		SecretEnv: map[string]string{
			"API_KEY": "agent-secrets",
		},
		ResourceRequests: engine.Resources{CPU: "500m", Memory: "512Mi"},
		ResourceLimits:   engine.Resources{CPU: "2", Memory: "4Gi"},
		Volumes: []engine.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
		},
		ActiveDeadlineSeconds: 3600,
	}
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name       string
		taskRunID  string
		engineName string
		spec       *engine.ExecutionSpec
		wantErr    bool
		errContain string
	}{
		{
			name:       "valid spec produces correct job",
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
				Image: "ghcr.io/robodev/agent:latest",
			},
			wantErr:    true,
			errContain: "missing required command",
		},
		{
			name:       "minimal spec with no optional fields",
			taskRunID:  "tr-minimal",
			engineName: "codex",
			spec: &engine.ExecutionSpec{
				Image:   "ghcr.io/robodev/codex:latest",
				Command: []string{"codex", "run"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewJobBuilder("default")
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

func TestBuild_Labels(t *testing.T) {
	builder := NewJobBuilder("agents")
	job, err := builder.Build("tr-456", "claude-code", validSpec())
	require.NoError(t, err)

	// Job-level labels.
	assert.Equal(t, "robodev-agent", job.Labels[labelApp])
	assert.Equal(t, "tr-456", job.Labels[labelTaskRunID])
	assert.Equal(t, "claude-code", job.Labels[labelEngine])

	// Pod template labels.
	podLabels := job.Spec.Template.Labels
	assert.Equal(t, "robodev-agent", podLabels[labelApp])
	assert.Equal(t, "tr-456", podLabels[labelTaskRunID])
	assert.Equal(t, "claude-code", podLabels[labelEngine])
}

func TestBuild_SecurityContext(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-sec", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	sc := container.SecurityContext
	require.NotNil(t, sc)

	assert.True(t, *sc.RunAsNonRoot, "runAsNonRoot should be true")
	assert.Equal(t, int64(1000), *sc.RunAsUser, "runAsUser should be 1000")
	assert.True(t, *sc.ReadOnlyRootFilesystem, "readOnlyRootFilesystem should be true")
	assert.False(t, *sc.AllowPrivilegeEscalation, "allowPrivilegeEscalation should be false")

	require.NotNil(t, sc.Capabilities)
	assert.Contains(t, sc.Capabilities.Drop, corev1.Capability("ALL"))

	require.NotNil(t, sc.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)
}

func TestBuild_Resources(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-res", "claude-code", validSpec())
	require.NoError(t, err)

	container := job.Spec.Template.Spec.Containers[0]
	reqs := container.Resources

	assert.Equal(t, resource.MustParse("500m"), reqs.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), reqs.Requests[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("2"), reqs.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("4Gi"), reqs.Limits[corev1.ResourceMemory])
}

func TestBuild_Namespace(t *testing.T) {
	builder := NewJobBuilder("robodev-agents")
	job, err := builder.Build("tr-ns", "codex", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "robodev-agents", job.Namespace)
}

func TestBuild_Volumes(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-vol", "claude-code", validSpec())
	require.NoError(t, err)

	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, "workspace", job.Spec.Template.Spec.Volumes[0].Name)

	container := job.Spec.Template.Spec.Containers[0]
	require.Len(t, container.VolumeMounts, 1)
	assert.Equal(t, "workspace", container.VolumeMounts[0].Name)
	assert.Equal(t, "/workspace", container.VolumeMounts[0].MountPath)
}

func TestBuild_ActiveDeadline(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-dl", "claude-code", validSpec())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int64(3600), *job.Spec.ActiveDeadlineSeconds)
}

func TestBuild_ActiveDeadline_ZeroOmitted(t *testing.T) {
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/robodev/agent:latest",
		Command: []string{"run"},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-no-dl", "claude-code", spec)
	require.NoError(t, err)

	assert.Nil(t, job.Spec.ActiveDeadlineSeconds)
}

func TestBuild_RestartPolicyAndBackoff(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-restart", "claude-code", validSpec())
	require.NoError(t, err)

	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
	require.NotNil(t, job.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
}

func TestBuild_Tolerations(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-tol", "claude-code", validSpec())
	require.NoError(t, err)

	tolerations := job.Spec.Template.Spec.Tolerations
	require.Len(t, tolerations, 1)
	assert.Equal(t, "robodev.io/agent", tolerations[0].Key)
	assert.Equal(t, corev1.TolerationOpExists, tolerations[0].Operator)
	assert.Equal(t, corev1.TaintEffectNoSchedule, tolerations[0].Effect)
}

func TestBuild_EnvVars(t *testing.T) {
	builder := NewJobBuilder("default")
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

func TestBuild_SecretEnv(t *testing.T) {
	builder := NewJobBuilder("default")
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

func TestBuild_JobName(t *testing.T) {
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-123", "claude-code", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "robodev-tr-123", job.Name)
}

func TestBuild_LongTaskRunID_Truncated(t *testing.T) {
	longID := "this-is-a-very-long-task-run-id-that-exceeds-sixty-three-characters-limit"
	builder := NewJobBuilder("default")
	job, err := builder.Build(longID, "claude-code", validSpec())
	require.NoError(t, err)

	assert.LessOrEqual(t, len(job.Name), 63, "job name must not exceed 63 characters")
}
