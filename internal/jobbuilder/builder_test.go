package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/unitaryai/osmia/pkg/engine"
)

func validSpec() *engine.ExecutionSpec {
	return &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
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
	assert.Equal(t, "osmia-agent", job.Labels[labelApp])
	assert.Equal(t, "agent", job.Labels[labelComponent])
	assert.Equal(t, "claude-code", job.Labels[labelEngine])
	assert.Equal(t, "osmia", job.Labels[labelManagedBy])
	assert.Equal(t, "tr-456", job.Labels[LabelTaskRunID])

	// Pod template labels.
	podLabels := job.Spec.Template.Labels
	assert.Equal(t, "osmia-agent", podLabels[labelApp])
	assert.Equal(t, "agent", podLabels[labelComponent])
	assert.Equal(t, "claude-code", podLabels[labelEngine])
	assert.Equal(t, "osmia", podLabels[labelManagedBy])
	assert.Equal(t, "tr-456", podLabels[LabelTaskRunID])
}

func TestBuild_SecurityContext(t *testing.T) {
	builder := NewJobBuilder("default")
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
	builder := NewJobBuilder("osmia-agents")
	job, err := builder.Build("tr-ns", "codex", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "osmia-agents", job.Namespace)
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
		Image:   "ghcr.io/osmia/agent:latest",
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
	assert.Equal(t, "osmia.io/agent", tolerations[0].Key)
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

	assert.Equal(t, "osmia-tr-123", job.Name)
}

func TestBuild_LongTaskRunID_Truncated(t *testing.T) {
	longID := "this-is-a-very-long-task-run-id-that-exceeds-sixty-three-characters-limit"
	builder := NewJobBuilder("default")
	job, err := builder.Build(longID, "claude-code", validSpec())
	require.NoError(t, err)

	assert.LessOrEqual(t, len(job.Name), 63, "job name must not exceed 63 characters")
}

func TestBuild_ConfigMapVolume(t *testing.T) {
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{
				Name:          "skill-changelog",
				MountPath:     "/skills/changelog.md",
				ReadOnly:      true,
				ConfigMapName: "my-skills-cm",
			},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-cm", "claude-code", spec)
	require.NoError(t, err)

	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	vol := job.Spec.Template.Spec.Volumes[0]
	assert.Equal(t, "skill-changelog", vol.Name)
	require.NotNil(t, vol.VolumeSource.ConfigMap, "volume should use ConfigMap source")
	assert.Nil(t, vol.VolumeSource.EmptyDir, "volume should not use EmptyDir")
	assert.Equal(t, "my-skills-cm", vol.VolumeSource.ConfigMap.Name)
	assert.Empty(t, vol.VolumeSource.ConfigMap.Items, "no key projection without ConfigMapKey")
}

func TestBuild_ConfigMapVolumeWithKey(t *testing.T) {
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{
				Name:          "skill-review",
				MountPath:     "/skills/review.md",
				ReadOnly:      true,
				SubPath:       "review.md",
				ConfigMapName: "review-cm",
				ConfigMapKey:  "review.md",
			},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-cmk", "claude-code", spec)
	require.NoError(t, err)

	vol := job.Spec.Template.Spec.Volumes[0]
	require.NotNil(t, vol.VolumeSource.ConfigMap)
	require.Len(t, vol.VolumeSource.ConfigMap.Items, 1)
	assert.Equal(t, "review.md", vol.VolumeSource.ConfigMap.Items[0].Key)
	assert.Equal(t, "review.md", vol.VolumeSource.ConfigMap.Items[0].Path)

	vm := job.Spec.Template.Spec.Containers[0].VolumeMounts[0]
	assert.Equal(t, "review.md", vm.SubPath)
	assert.True(t, vm.ReadOnly)
}

func TestBuild_PVCVolume(t *testing.T) {
	// A VolumeMount with PVCName must produce a PVC-backed volume source, not EmptyDir.
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{
				Name:      "session-claude",
				MountPath: "/session",
				SubPath:   "tr-abc/claude",
				PVCName:   "osmia-agent-sessions",
			},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-pvc", "claude-code", spec)
	require.NoError(t, err)

	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	vol := job.Spec.Template.Spec.Volumes[0]
	assert.Equal(t, "session-claude", vol.Name)
	require.NotNil(t, vol.VolumeSource.PersistentVolumeClaim, "volume must use PVC source")
	assert.Nil(t, vol.VolumeSource.EmptyDir, "volume must not use EmptyDir")
	assert.Nil(t, vol.VolumeSource.ConfigMap, "volume must not use ConfigMap")
	assert.Equal(t, "osmia-agent-sessions", vol.VolumeSource.PersistentVolumeClaim.ClaimName)

	vm := job.Spec.Template.Spec.Containers[0].VolumeMounts[0]
	assert.Equal(t, "session-claude", vm.Name)
	assert.Equal(t, "/session", vm.MountPath)
	assert.Equal(t, "tr-abc/claude", vm.SubPath)
}

func TestBuild_SharedPVCDeduplication(t *testing.T) {
	// When two VolumeMount entries reference the same PVC, only one K8s Volume must be
	// emitted. Both VolumeMounts must reference that single volume by name. Without
	// deduplication the kubelet deadlocks waiting for NodePublishVolume on a claim it
	// considers already in-use.
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{
				Name:      "session-claude",
				MountPath: "/session",
				SubPath:   "claude",
				PVCName:   "osmia-session-tr-123",
			},
			{
				Name:      "session-workspace",
				MountPath: "/workspace-persist",
				SubPath:   "workspace",
				PVCName:   "osmia-session-tr-123",
			},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-shared-pvc", "claude-code", spec)
	require.NoError(t, err)

	podSpec := job.Spec.Template.Spec

	// Exactly one Volume must be declared despite two mounts using the same PVC.
	require.Len(t, podSpec.Volumes, 1, "duplicate PVC must not produce two Volume specs")
	vol := podSpec.Volumes[0]
	assert.Equal(t, "session-claude", vol.Name)
	require.NotNil(t, vol.VolumeSource.PersistentVolumeClaim)
	assert.Equal(t, "osmia-session-tr-123", vol.VolumeSource.PersistentVolumeClaim.ClaimName)

	// Both VolumeMounts must reference the single declared volume.
	vms := podSpec.Containers[0].VolumeMounts
	require.Len(t, vms, 2)
	assert.Equal(t, "session-claude", vms[0].Name)
	assert.Equal(t, "/session", vms[0].MountPath)
	assert.Equal(t, "claude", vms[0].SubPath)
	assert.Equal(t, "session-claude", vms[1].Name, "second mount must reuse first volume name")
	assert.Equal(t, "/workspace-persist", vms[1].MountPath)
	assert.Equal(t, "workspace", vms[1].SubPath)
}

func TestBuild_PVCVolumeOverridesConfigMap(t *testing.T) {
	// PVCName takes precedence over ConfigMapName when both are set.
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{
				Name:          "mixed",
				MountPath:     "/data",
				PVCName:       "my-pvc",
				ConfigMapName: "my-cm",
			},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-pvc-cm", "claude-code", spec)
	require.NoError(t, err)

	vol := job.Spec.Template.Spec.Volumes[0]
	require.NotNil(t, vol.VolumeSource.PersistentVolumeClaim)
	assert.Nil(t, vol.VolumeSource.ConfigMap)
	assert.Equal(t, "my-pvc", vol.VolumeSource.PersistentVolumeClaim.ClaimName)
}

func TestBuild_PodSecurityContextFSGroup(t *testing.T) {
	// The pod security context must set fsGroup so that Kubernetes chowns PVC-backed
	// volume directories to be writable by the non-root container user. Without fsGroup,
	// freshly formatted EBS volumes are owned by root and writes fail with EACCES.
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-sc", "claude-code", validSpec())
	require.NoError(t, err)

	sc := job.Spec.Template.Spec.SecurityContext
	require.NotNil(t, sc, "pod security context must be set")
	require.NotNil(t, sc.FSGroup, "fsGroup must be set")
	assert.Equal(t, int64(defaultRunAsUser), *sc.FSGroup)
}

func TestBuild_MixedVolumes(t *testing.T) {
	spec := &engine.ExecutionSpec{
		Image:   "ghcr.io/osmia/agent:latest",
		Command: []string{"run"},
		Volumes: []engine.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "skill-cm", MountPath: "/skills/s.md", ReadOnly: true, ConfigMapName: "skills-cm", ConfigMapKey: "s.md", SubPath: "s.md"},
		},
	}
	builder := NewJobBuilder("default")
	job, err := builder.Build("tr-mix", "claude-code", spec)
	require.NoError(t, err)

	require.Len(t, job.Spec.Template.Spec.Volumes, 2)
	assert.NotNil(t, job.Spec.Template.Spec.Volumes[0].VolumeSource.EmptyDir)
	assert.NotNil(t, job.Spec.Template.Spec.Volumes[1].VolumeSource.ConfigMap)
}
