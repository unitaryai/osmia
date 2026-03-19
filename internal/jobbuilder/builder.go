// Package jobbuilder translates engine ExecutionSpecs into Kubernetes
// batch/v1 Jobs with appropriate security contexts, volumes, and labels.
package jobbuilder

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/pkg/engine"
)

// LabelTaskRunID is the Kubernetes label key used to identify Jobs and Pods
// belonging to a given task run. It is exported so that the controller can
// list pods by this label when resolving pod names for log streaming.
const LabelTaskRunID = "osmia.io/task-run-id"

const (
	labelApp      = "app"
	labelAppValue = "osmia-agent"
	labelEngine   = "osmia.io/engine"

	// labelComponent and labelManagedBy are Kubernetes recommended labels that
	// identify agent pods for NetworkPolicy and other selectors.
	labelComponent = "app.kubernetes.io/component"
	labelManagedBy = "app.kubernetes.io/managed-by"
	componentAgent = "agent"
	managedByOsmia = "osmia"

	defaultRunAsUser int64 = 10000
	containerName          = "agent"
	taintKey               = "osmia.io/agent"
)

// JobBuilder constructs Kubernetes Jobs from engine ExecutionSpecs.
type JobBuilder struct {
	namespace string
}

// NewJobBuilder creates a new JobBuilder that creates Jobs in the given namespace.
func NewJobBuilder(namespace string) *JobBuilder {
	return &JobBuilder{namespace: namespace}
}

// Build translates an ExecutionSpec into a Kubernetes batch/v1.Job with
// security contexts, labels, tolerations, and resource limits applied.
func (b *JobBuilder) Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
	if spec.Image == "" {
		return nil, fmt.Errorf("execution spec missing required image")
	}
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("execution spec missing required command")
	}

	envVars := buildEnvVars(spec.Env)
	envVars = append(envVars, buildSecretKeyRefVars(spec.SecretKeyRefs)...)
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

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: b.namespace,
			Labels: map[string]string{
				labelApp:       labelAppValue,
				labelComponent: componentAgent,
				labelEngine:    engineName,
				labelManagedBy: managedByOsmia,
				LabelTaskRunID: taskRunID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						labelApp:       labelAppValue,
						labelComponent: componentAgent,
						labelEngine:    engineName,
						labelManagedBy: managedByOsmia,
						LabelTaskRunID: taskRunID,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
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

// buildSecretKeyRefVars converts a map of secret key references into Kubernetes
// EnvVar entries using valueFrom.secretKeyRef. This allows an env var name to
// be mapped to a specific key inside a named Secret, without mounting the
// entire secret via envFrom.
func buildSecretKeyRefVars(refs map[string]engine.SecretKeyRef) []corev1.EnvVar {
	if len(refs) == 0 {
		return nil
	}
	vars := make([]corev1.EnvVar, 0, len(refs))
	for envVarName, ref := range refs {
		vars = append(vars, corev1.EnvVar{
			Name: envVarName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ref.SecretName},
					Key:                  ref.Key,
				},
			},
		})
	}
	return vars
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
// Each key in the map is the environment variable name and each value is the
// Kubernetes Secret name to source it from.
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
// The volume source is chosen in priority order: PVCName > ConfigMapName > emptyDir.
// When ConfigMapKey is also set alongside ConfigMapName, only that key is projected.
//
// PVC deduplication: when two mounts share the same PVCName, only one K8s Volume is
// emitted for that claim. Subsequent mounts reuse the first volume's name so that the
// kubelet volume manager sees a single PVC and can call NodePublishVolume once. Without
// this, the kubelet deadlocks when two Volume specs reference identical claims.
func buildVolumes(mounts []engine.VolumeMount) ([]corev1.Volume, []corev1.VolumeMount) {
	if len(mounts) == 0 {
		return nil, nil
	}
	// pvcVolumeName maps a PVC claim name to the first Volume name declared for it.
	pvcVolumeName := make(map[string]string)
	volumes := make([]corev1.Volume, 0, len(mounts))
	volumeMounts := make([]corev1.VolumeMount, 0, len(mounts))
	for _, m := range mounts {
		// volumeName is the name used in the VolumeMount; it may differ from m.Name
		// when this PVC has already been declared as a Volume.
		volumeName := m.Name

		if m.PVCName != "" {
			if existing, ok := pvcVolumeName[m.PVCName]; ok {
				// Reuse the already-declared volume rather than emitting a duplicate.
				volumeName = existing
			} else {
				pvcVolumeName[m.PVCName] = m.Name
				volumes = append(volumes, corev1.Volume{
					Name: m.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: m.PVCName,
						},
					},
				})
			}
		} else if m.ConfigMapName != "" {
			cmSource := &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: m.ConfigMapName},
			}
			if m.ConfigMapKey != "" {
				cmSource.Items = []corev1.KeyToPath{
					{Key: m.ConfigMapKey, Path: m.ConfigMapKey},
				}
			}
			volumes = append(volumes, corev1.Volume{
				Name:         m.Name,
				VolumeSource: corev1.VolumeSource{ConfigMap: cmSource},
			})
		} else {
			volumes = append(volumes, corev1.Volume{
				Name:         m.Name,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
		}

		vm := corev1.VolumeMount{
			Name:      volumeName,
			MountPath: m.MountPath,
			ReadOnly:  m.ReadOnly,
		}
		if m.SubPath != "" {
			vm.SubPath = m.SubPath
		}
		volumeMounts = append(volumeMounts, vm)
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
