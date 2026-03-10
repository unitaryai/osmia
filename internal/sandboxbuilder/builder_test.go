package sandboxbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/unitaryai/osmia/internal/config"
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

func defaultSandboxConfig() config.SandboxConfig {
	return config.SandboxConfig{
		RuntimeClass: "gvisor",
		EnvStripping: true,
		WarmPool: config.WarmPoolConfig{
			Enabled: true,
			Size:    3,
		},
	}
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name       string
		taskRunID  string
		engineName string
		spec       *engine.ExecutionSpec
		cfg        config.SandboxConfig
		wantErr    bool
		errContain string
	}{
		{
			name:       "valid spec produces correct sandboxed job",
			taskRunID:  "tr-123",
			engineName: "claude-code",
			spec:       validSpec(),
			cfg:        defaultSandboxConfig(),
		},
		{
			name:       "missing image returns error",
			taskRunID:  "tr-123",
			engineName: "claude-code",
			spec: &engine.ExecutionSpec{
				Command: []string{"echo", "hello"},
			},
			cfg:        defaultSandboxConfig(),
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
			cfg:        defaultSandboxConfig(),
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
			cfg: defaultSandboxConfig(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := New("default", tt.cfg)
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

func TestBuild_DefaultGVisorRuntimeClass(t *testing.T) {
	builder := New("default", config.SandboxConfig{})
	job, err := builder.Build("tr-default-rc", "claude-code", validSpec())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "gvisor", *job.Spec.Template.Spec.RuntimeClassName)
}

func TestBuild_KataRuntimeClassOverride(t *testing.T) {
	cfg := config.SandboxConfig{
		RuntimeClass: "kata",
	}
	builder := New("default", cfg)
	job, err := builder.Build("tr-kata", "claude-code", validSpec())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "kata", *job.Spec.Template.Spec.RuntimeClassName)
}

func TestBuild_WarmPoolLabels(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.SandboxConfig
		engineName string
		wantLabel  bool
	}{
		{
			name: "warm pool enabled adds label",
			cfg: config.SandboxConfig{
				RuntimeClass: "gvisor",
				WarmPool:     config.WarmPoolConfig{Enabled: true, Size: 5},
			},
			engineName: "claude-code",
			wantLabel:  true,
		},
		{
			name: "warm pool disabled omits label",
			cfg: config.SandboxConfig{
				RuntimeClass: "gvisor",
				WarmPool:     config.WarmPoolConfig{Enabled: false},
			},
			engineName: "claude-code",
			wantLabel:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := New("default", tt.cfg)
			job, err := builder.Build("tr-wp", tt.engineName, validSpec())
			require.NoError(t, err)

			podLabels := job.Spec.Template.Labels
			if tt.wantLabel {
				assert.Equal(t, tt.engineName, podLabels[labelWarmPool])
			} else {
				_, exists := podLabels[labelWarmPool]
				assert.False(t, exists, "warm pool label should not be present when disabled")
			}
		})
	}
}

func TestBuild_SandboxClaimAnnotation(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
	job, err := builder.Build("tr-claim", "claude-code", validSpec())
	require.NoError(t, err)

	annotations := job.Spec.Template.Annotations
	assert.Equal(t, "true", annotations[annotationSandboxClaim])
}

func TestBuild_SecurityContext(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
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

func TestBuild_EnvStrippingAnnotation(t *testing.T) {
	tests := []struct {
		name           string
		envStripping   bool
		wantAnnotation bool
	}{
		{
			name:           "env stripping enabled adds annotation",
			envStripping:   true,
			wantAnnotation: true,
		},
		{
			name:           "env stripping disabled omits annotation",
			envStripping:   false,
			wantAnnotation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.SandboxConfig{
				RuntimeClass: "gvisor",
				EnvStripping: tt.envStripping,
			}
			builder := New("default", cfg)
			job, err := builder.Build("tr-strip", "claude-code", validSpec())
			require.NoError(t, err)

			annotations := job.Spec.Template.Annotations
			if tt.wantAnnotation {
				assert.Equal(t, "true", annotations[annotationEnvStripping])
			} else {
				_, exists := annotations[annotationEnvStripping]
				assert.False(t, exists, "env stripping annotation should not be present when disabled")
			}
		})
	}
}

func TestBuild_Resources(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
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
	builder := New("osmia-agents", defaultSandboxConfig())
	job, err := builder.Build("tr-ns", "codex", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "osmia-agents", job.Namespace)
}

func TestBuild_JobName(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
	job, err := builder.Build("tr-123", "claude-code", validSpec())
	require.NoError(t, err)

	assert.Equal(t, "osmia-tr-123", job.Name)
}

func TestBuild_LongTaskRunID_Truncated(t *testing.T) {
	longID := "this-is-a-very-long-task-run-id-that-exceeds-sixty-three-characters-limit"
	builder := New("default", defaultSandboxConfig())
	job, err := builder.Build(longID, "claude-code", validSpec())
	require.NoError(t, err)

	assert.LessOrEqual(t, len(job.Name), 63, "job name must not exceed 63 characters")
}

func TestBuild_Tolerations(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
	job, err := builder.Build("tr-tol", "claude-code", validSpec())
	require.NoError(t, err)

	tolerations := job.Spec.Template.Spec.Tolerations
	require.Len(t, tolerations, 1)
	assert.Equal(t, "osmia.io/agent", tolerations[0].Key)
	assert.Equal(t, corev1.TolerationOpExists, tolerations[0].Operator)
	assert.Equal(t, corev1.TaintEffectNoSchedule, tolerations[0].Effect)
}

func TestBuild_ActiveDeadline(t *testing.T) {
	builder := New("default", defaultSandboxConfig())
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
	builder := New("default", defaultSandboxConfig())
	job, err := builder.Build("tr-no-dl", "claude-code", spec)
	require.NoError(t, err)

	assert.Nil(t, job.Spec.ActiveDeadlineSeconds)
}
