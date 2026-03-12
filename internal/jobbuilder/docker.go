// Package jobbuilder translates engine ExecutionSpecs into Kubernetes
// batch/v1 Jobs with appropriate security contexts, volumes, and labels.

package jobbuilder

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// annotationBackend signals which execution backend produced this job.
	annotationBackend = "osmia.io/execution-backend"
)

// DockerBuilder constructs Kubernetes Job objects annotated for local Docker
// execution. It implements the same controller.JobBuilder interface as the
// standard JobBuilder but marks jobs with a "local" backend annotation so
// that a local runner can identify and execute them via Docker instead of
// the Kubernetes API.
type DockerBuilder struct {
	namespace string
}

// NewDockerBuilder creates a DockerBuilder that produces Jobs in the given namespace.
func NewDockerBuilder(namespace string) *DockerBuilder {
	return &DockerBuilder{namespace: namespace}
}

// Build translates an ExecutionSpec into a Kubernetes batch/v1.Job annotated
// with the local Docker execution backend. Security contexts, volumes, and
// resource requirements are preserved to maintain parity with production jobs.
func (b *DockerBuilder) Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
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
				LabelTaskRunID: taskRunID,
				labelEngine:    engineName,
			},
			Annotations: map[string]string{
				annotationBackend: "local",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						labelApp:       labelAppValue,
						LabelTaskRunID: taskRunID,
						labelEngine:    engineName,
					},
					Annotations: map[string]string{
						annotationBackend: "local",
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
