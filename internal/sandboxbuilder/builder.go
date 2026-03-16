// Package sandboxbuilder translates engine ExecutionSpecs into Kubernetes
// batch/v1 Jobs configured for sandboxed execution using gVisor (or similar
// container runtimes) with optional warm pool integration.
package sandboxbuilder

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	labelApp       = "app"
	labelAppValue  = "osmia-agent"
	labelTaskRunID = "osmia.io/task-run-id"
	labelEngine    = "osmia.io/engine"

	// labelComponent and labelManagedBy are Kubernetes recommended labels that
	// identify agent pods for NetworkPolicy and other selectors.
	labelComponent = "app.kubernetes.io/component"
	labelManagedBy = "app.kubernetes.io/managed-by"
	componentAgent = "agent"
	managedByOsmia = "osmia"

	// annotationSandboxClaim signals the sandbox controller to bind a
	// pre-allocated sandbox to this pod via the SandboxClaim abstraction.
	annotationSandboxClaim = "sandbox.kubernetes.io/claim"

	// annotationEnvStripping indicates the entrypoint should strip sensitive
	// environment variables after use.
	annotationEnvStripping = "osmia.io/env-stripping"

	// labelWarmPool identifies which warm pool a pod should be drawn from.
	labelWarmPool = "sandbox.kubernetes.io/warm-pool"

	defaultRunAsUser int64 = 1000
	containerName          = "agent"
	taintKey               = "osmia.io/agent"
)

// SandboxBuilder constructs Kubernetes Jobs from engine ExecutionSpecs with
// gVisor runtime isolation and optional warm pool scheduling.
type SandboxBuilder struct {
	namespace string
	cfg       config.SandboxConfig
}

// New creates a SandboxBuilder that produces Jobs in the given namespace
// using the provided sandbox configuration.
func New(namespace string, cfg config.SandboxConfig) *SandboxBuilder {
	if cfg.RuntimeClass == "" {
		cfg.RuntimeClass = "gvisor"
	}
	return &SandboxBuilder{namespace: namespace, cfg: cfg}
}

// Build translates an ExecutionSpec into a Kubernetes batch/v1.Job configured
// for sandboxed execution. The resulting Job uses a RuntimeClassName for
// container-level isolation and includes annotations for the SandboxClaim
// abstraction.
func (b *SandboxBuilder) Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
	if spec.Image == "" {
		return nil, fmt.Errorf("execution spec missing required image")
	}
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("execution spec missing required command")
	}

	envVars := buildEnvVars(spec.Env)
	envFromSources := buildEnvFromSources(spec.SecretEnv)
	volumes, volumeMounts := buildVolumes(spec.Volumes)
	resources := buildResourceRequirements(spec.ResourceRequests, spec.ResourceLimits)

	backoffLimit := int32(0)
	runAsNonRoot := true
	runAsUser := defaultRunAsUser
	readOnlyRootFS := true
	activeDeadline := int64(spec.ActiveDeadlineSeconds)

	seccompProfile := &corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}

	jobName := fmt.Sprintf("osmia-%s", taskRunID)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	// Build pod annotations.
	podAnnotations := map[string]string{
		annotationSandboxClaim: "true",
	}
	if b.cfg.EnvStripping {
		podAnnotations[annotationEnvStripping] = "true"
	}

	// Build pod labels.
	podLabels := map[string]string{
		labelApp:       labelAppValue,
		labelComponent: componentAgent,
		labelEngine:    engineName,
		labelManagedBy: managedByOsmia,
		labelTaskRunID: taskRunID,
	}
	if b.cfg.WarmPool.Enabled {
		podLabels[labelWarmPool] = engineName
	}

	runtimeClass := b.cfg.RuntimeClass

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: b.namespace,
			Labels: map[string]string{
				labelApp:       labelAppValue,
				labelComponent: componentAgent,
				labelEngine:    engineName,
				labelManagedBy: managedByOsmia,
				labelTaskRunID: taskRunID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					RuntimeClassName: &runtimeClass,
					RestartPolicy:    corev1.RestartPolicyNever,
					Tolerations: []corev1.Toleration{
						{
							Key:      taintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
					Containers: []corev1.Container{
						{
							Name:         containerName,
							Image:        spec.Image,
							Command:      spec.Command,
							Env:          envVars,
							EnvFrom:      envFromSources,
							Resources:    resources,
							VolumeMounts: volumeMounts,
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             &runAsNonRoot,
								RunAsUser:                &runAsUser,
								ReadOnlyRootFilesystem:   &readOnlyRootFS,
								AllowPrivilegeEscalation: ptrBool(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								SeccompProfile: seccompProfile,
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	if spec.ActiveDeadlineSeconds > 0 {
		job.Spec.ActiveDeadlineSeconds = &activeDeadline
	}

	return job, nil
}

// buildEnvVars converts a map of environment variables to Kubernetes EnvVar slice.
func buildEnvVars(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	vars := make([]corev1.EnvVar, 0, len(env))
	for k, v := range env {
		vars = append(vars, corev1.EnvVar{Name: k, Value: v})
	}
	return vars
}

// buildEnvFromSources converts a map of secret names to EnvFromSource slice.
func buildEnvFromSources(secretEnv map[string]string) []corev1.EnvFromSource {
	if len(secretEnv) == 0 {
		return nil
	}
	sources := make([]corev1.EnvFromSource, 0, len(secretEnv))
	for _, secretName := range secretEnv {
		sources = append(sources, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
			},
		})
	}
	return sources
}

// buildVolumes converts engine VolumeMount specs into K8s Volumes and VolumeMounts.
func buildVolumes(mounts []engine.VolumeMount) ([]corev1.Volume, []corev1.VolumeMount) {
	if len(mounts) == 0 {
		return nil, nil
	}
	volumes := make([]corev1.Volume, 0, len(mounts))
	volumeMounts := make([]corev1.VolumeMount, 0, len(mounts))
	for _, m := range mounts {
		volumes = append(volumes, corev1.Volume{
			Name: m.Name,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      m.Name,
			MountPath: m.MountPath,
			ReadOnly:  m.ReadOnly,
		})
	}
	return volumes, volumeMounts
}

// buildResourceRequirements converts engine Resources to K8s ResourceRequirements.
func buildResourceRequirements(requests, limits engine.Resources) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{}

	if requests.CPU != "" || requests.Memory != "" {
		reqs.Requests = corev1.ResourceList{}
		if requests.CPU != "" {
			reqs.Requests[corev1.ResourceCPU] = resource.MustParse(requests.CPU)
		}
		if requests.Memory != "" {
			reqs.Requests[corev1.ResourceMemory] = resource.MustParse(requests.Memory)
		}
	}

	if limits.CPU != "" || limits.Memory != "" {
		reqs.Limits = corev1.ResourceList{}
		if limits.CPU != "" {
			reqs.Limits[corev1.ResourceCPU] = resource.MustParse(limits.CPU)
		}
		if limits.Memory != "" {
			reqs.Limits[corev1.ResourceMemory] = resource.MustParse(limits.Memory)
		}
	}

	return reqs
}

func ptrBool(b bool) *bool {
	return &b
}
