package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/k8sname"
	"github.com/unitaryai/osmia/internal/secretresolver"
	"github.com/unitaryai/osmia/pkg/engine"
)

// taskSecretNamePrefix prefixes the ephemeral Kubernetes Secret that holds
// the resolved values of a TaskRun's task-scoped secrets. The Secret is
// owned by the agent Job, so Kubernetes garbage-collects it when the Job is
// deleted.
const taskSecretNamePrefix = "osmia-task-secrets-"

// Labels applied to every ephemeral task Secret. labelSecretPurpose is what
// the orphan sweep selects on: matching by name prefix is not possible in a
// label selector, and managed-by alone would also match any other Secret
// Osmia comes to own.
const (
	labelComponent = "app.kubernetes.io/component"
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelEngine    = "osmia.io/engine"

	labelSecretPurpose = "osmia.io/secret-purpose"

	componentAgent          = "agent"
	managedByOsmia          = "osmia"
	secretPurposeTaskSecret = "task-secrets"
)

// taskSecrets is the outcome of resolving a task's secret references: the
// env-name-to-SecretKeyRef mapping to merge into the ExecutionSpec, plus the
// resolved raw values that must first be written to an ephemeral Secret.
type taskSecrets struct {
	// KeyRefs maps environment variable names to the Kubernetes Secret key
	// they should be injected from.
	KeyRefs map[string]engine.SecretKeyRef

	// Values holds resolved plaintext secret values keyed by environment
	// variable name. These come from non-Kubernetes backends (Vault, AWS
	// Secrets Manager) and are written to the ephemeral Secret rather than
	// inlined into the Job manifest.
	Values map[string]string
}

// resolveTaskSecrets parses task-scoped secret references from a task's
// description and labels, resolves them through the configured resolver, and
// returns the injection plan.
//
// It returns a nil plan when no resolver is configured or the task declares
// no secrets. Any policy violation or backend failure is returned as an
// error: secret resolution is fail-closed, so a task that asks for a secret
// it may not have is never launched without it.
func (r *Reconciler) resolveTaskSecrets(ctx context.Context, task engine.Task) (*taskSecrets, error) {
	if r.secretsResolver == nil {
		return nil, nil
	}

	requests, err := taskSecretRequests(task)
	if err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, nil
	}

	resolved, err := r.secretsResolver.Resolve(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("resolving task-scoped secrets: %w", err)
	}

	plan := &taskSecrets{
		KeyRefs: make(map[string]engine.SecretKeyRef),
		Values:  make(map[string]string),
	}
	for _, rs := range resolved {
		if rs.EnvName == "" {
			return nil, fmt.Errorf("resolved secret has no environment variable name")
		}
		if rs.SecretKeyRef != nil {
			plan.KeyRefs[rs.EnvName] = engine.SecretKeyRef{
				SecretName: rs.SecretKeyRef.SecretName,
				Key:        rs.SecretKeyRef.Key,
			}
			continue
		}
		plan.Values[rs.EnvName] = rs.Value
	}

	return plan, nil
}

// taskSecretRequests collects the secret requests declared by a task, from
// both the osmia:secrets comment block in its description and any
// osmia:secret:ENV=URI labels.
func taskSecretRequests(task engine.Task) ([]secretresolver.SecretRequest, error) {
	fromBody, err := secretresolver.ParseCommentBlock(task.Description)
	if err != nil {
		return nil, fmt.Errorf("parsing secret block from task description: %w", err)
	}

	fromLabels, err := secretresolver.ParseLabels(task.Labels)
	if err != nil {
		return nil, fmt.Errorf("parsing secret labels: %w", err)
	}

	return append(fromBody, fromLabels...), nil
}

// applyTaskSecrets merges a resolved secret plan into an ExecutionSpec.
//
// Values resolved from non-Kubernetes backends are not written into the spec
// directly: the caller is expected to have staged them into an ephemeral
// Secret named secretName, and this function points the corresponding env
// vars at that Secret. Plaintext secrets therefore never appear in the Job
// manifest.
//
// A task may not shadow an environment variable the engine already sets;
// doing so would let a ticket author override the agent's own credentials
// (for example ANTHROPIC_API_KEY). Collisions are rejected rather than
// silently resolved in either direction.
func applyTaskSecrets(spec *engine.ExecutionSpec, plan *taskSecrets, secretName string) error {
	if plan == nil {
		return nil
	}

	for _, envName := range sortedEnvNames(plan) {
		if err := checkEnvCollision(spec, envName); err != nil {
			return err
		}
	}

	if spec.SecretKeyRefs == nil {
		spec.SecretKeyRefs = make(map[string]engine.SecretKeyRef)
	}
	for envName, ref := range plan.KeyRefs {
		spec.SecretKeyRefs[envName] = ref
	}
	for envName := range plan.Values {
		spec.SecretKeyRefs[envName] = engine.SecretKeyRef{
			SecretName: secretName,
			Key:        envName,
		}
	}

	return nil
}

// sortedEnvNames returns every environment variable name in the plan in a
// stable order, so that a task declaring several colliding secrets always
// fails on the same one.
func sortedEnvNames(plan *taskSecrets) []string {
	names := make([]string, 0, len(plan.KeyRefs)+len(plan.Values))
	for envName := range plan.KeyRefs {
		names = append(names, envName)
	}
	for envName := range plan.Values {
		names = append(names, envName)
	}
	sort.Strings(names)
	return names
}

// checkEnvCollision reports an error when envName is already supplied by the
// engine's own execution spec.
func checkEnvCollision(spec *engine.ExecutionSpec, envName string) error {
	if _, ok := spec.Env[envName]; ok {
		return fmt.Errorf("task-scoped secret %q collides with an environment variable set by the engine", envName)
	}
	if _, ok := spec.SecretKeyRefs[envName]; ok {
		return fmt.Errorf("task-scoped secret %q collides with a secret reference set by the engine", envName)
	}
	// Known limitation. spec.SecretEnv maps a name to a Kubernetes Secret
	// that the job builder mounts with envFrom, which injects *every* key in
	// that Secret as an environment variable. The Go map key is a naming
	// convention, not an enumeration of what the Secret actually contains,
	// so this check cannot see a key the Secret holds under some other name.
	// Because Kubernetes gives an explicit env[] entry precedence over
	// envFrom, a task naming such a key would shadow it and this guard would
	// not notice.
	//
	// Closing it properly would mean the controller reading every referenced
	// Secret at launch, which trades a real cost for a case the env-name
	// policy already covers: blocked_env_patterns is the intended control for
	// "tasks may never name this variable", and it does not depend on
	// knowing what any Secret contains. Documented in
	// docs/getting-started/configuration.md rather than papered over here.
	if _, ok := spec.SecretEnv[envName]; ok {
		return fmt.Errorf("task-scoped secret %q collides with a secret mount set by the engine", envName)
	}
	return nil
}

// taskSecretName returns the name of the ephemeral Secret holding a
// TaskRun's resolved secret values.
func taskSecretName(taskRunID string) string {
	return k8sname.Subdomain(taskSecretNamePrefix, taskRunID)
}

// createTaskSecret writes the plan's resolved values to an ephemeral
// Kubernetes Secret. It is a no-op when the plan holds no raw values, since
// Kubernetes-backed references are injected from their existing Secrets.
//
// The Secret is created before the Job so the kubelet never has to retry a
// missing reference; adoptTaskSecret then makes the Job its owner.
func (r *Reconciler) createTaskSecret(ctx context.Context, taskRunID, engineName string, plan *taskSecrets) error {
	if r.k8sClient == nil || plan == nil || len(plan.Values) == 0 {
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskSecretName(taskRunID),
			Namespace: r.namespace,
			Labels: map[string]string{
				labelComponent:            componentAgent,
				labelManagedBy:            managedByOsmia,
				labelSecretPurpose:        secretPurposeTaskSecret,
				labelEngine:               engineName,
				jobbuilder.LabelTaskRunID: taskRunID,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: plan.Values,
	}

	_, err := r.k8sClient.CoreV1().Secrets(r.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating task secret for task run %s: %w", taskRunID, err)
	}
	return nil
}

// taskSecretCleanupTimeout bounds the detached context used for the two
// secret operations that must still run when the launch context is being
// torn down.
const taskSecretCleanupTimeout = 15 * time.Second

// detachedCleanupContext returns a bounded context that survives ctx's
// cancellation, keeping ctx's values (trace and log correlation) but not its
// deadline. Both remaining secret operations are finishing-up work: by the
// time they run, an ephemeral Secret holding plaintext already exists in the
// cluster, and inheriting a cancelled context would leave it there.
func detachedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), taskSecretCleanupTimeout)
}

// adoptTaskSecret sets job as the owner of the TaskRun's ephemeral Secret so
// that deleting the Job garbage-collects the Secret with it. A failure here
// leaves an orphaned Secret rather than a broken run, so it is logged by the
// caller and not treated as fatal.
//
// It runs on a detached context: the Job already exists and will run whatever
// happens to the caller's context, so abandoning adoption on cancellation
// would orphan the Secret permanently.
func (r *Reconciler) adoptTaskSecret(ctx context.Context, taskRunID string, job *batchv1.Job, plan *taskSecrets) error {
	if r.k8sClient == nil || plan == nil || len(plan.Values) == 0 {
		return nil
	}

	ctx, cancel := detachedCleanupContext(ctx)
	defer cancel()

	name := taskSecretName(taskRunID)
	secrets := r.k8sClient.CoreV1().Secrets(r.namespace)

	secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching task secret %s: %w", name, err)
	}

	secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       job.Name,
		UID:        job.UID,
	})

	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("setting owner on task secret %s: %w", name, err)
	}
	return nil
}

// discardTaskSecret removes an ephemeral Secret that was written for a
// launch which then failed before the Job existed to own it. The launch is
// already being aborted, so a cleanup failure is logged rather than
// returned; it would otherwise mask the error that caused the abort.
func (r *Reconciler) discardTaskSecret(ctx context.Context, taskRunID string, plan *taskSecrets) {
	if err := r.deleteTaskSecret(ctx, taskRunID, plan); err != nil {
		r.logger.WarnContext(ctx, "failed to clean up task secret after aborted launch",
			"task_run_id", taskRunID,
			"error", err,
		)
	}
}

// deleteTaskSecret removes the TaskRun's ephemeral Secret. It is used to
// clean up when Job creation fails after the Secret was already written, at
// which point no Job exists to own it.
//
// It runs on a detached context. A launch aborted by cancellation is exactly
// when this matters most: inheriting the cancelled context would fail the
// delete and leave a Secret full of plaintext credentials with no owner to
// garbage-collect it.
func (r *Reconciler) deleteTaskSecret(ctx context.Context, taskRunID string, plan *taskSecrets) error {
	if r.k8sClient == nil || plan == nil || len(plan.Values) == 0 {
		return nil
	}

	ctx, cancel := detachedCleanupContext(ctx)
	defer cancel()

	name := taskSecretName(taskRunID)
	if err := r.k8sClient.CoreV1().Secrets(r.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("deleting task secret %s: %w", name, err)
	}
	return nil
}

// taskSecretOrphanGrace is how long an unadopted ephemeral Secret is left
// alone before the sweep treats it as stranded. Adoption normally happens
// within a second or so of creation, immediately after the Job is created,
// so this is far longer than the legitimate window; it only has to exceed
// the worst case of a slow Job create against a struggling API server.
const taskSecretOrphanGrace = 15 * time.Minute

// sweepOrphanedTaskSecrets deletes ephemeral task Secrets that were created
// for a launch which then died before the Job could adopt them.
//
// A Secret that reached adoption carries an ownerReference to its Job, and
// Kubernetes garbage-collects it when that Job goes away. The abort paths in
// launchTaskRun delete the Secret when a launch fails in-process. Neither
// covers the controller being killed between creating the Secret and either
// of those happening, which strands a Secret full of plaintext credentials
// that nothing will ever collect.
//
// The sweep therefore looks only for Secrets with no ownerReferences at all,
// past the grace period. That needs no cross-referencing against live Jobs
// and cannot race with an adoption in progress: a Secret either has an owner,
// in which case Kubernetes owns its lifecycle, or it never got one and no
// longer will.
//
// Failures are logged rather than returned. This runs on every reconcile
// tick, so a transient API error simply means the next tick tries again.
func (r *Reconciler) sweepOrphanedTaskSecrets(ctx context.Context) {
	if r.k8sClient == nil {
		return
	}

	secrets := r.k8sClient.CoreV1().Secrets(r.namespace)
	list, err := secrets.List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			labelManagedBy:     managedByOsmia,
			labelSecretPurpose: secretPurposeTaskSecret,
		}.String(),
	})
	if err != nil {
		r.logger.WarnContext(ctx, "failed to list task secrets for orphan sweep", "error", err)
		return
	}

	for i := range list.Items {
		secret := &list.Items[i]
		if len(secret.OwnerReferences) > 0 {
			continue
		}
		if time.Since(secret.CreationTimestamp.Time) < taskSecretOrphanGrace {
			continue
		}

		if err := secrets.Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				r.logger.WarnContext(ctx, "failed to delete orphaned task secret",
					"secret", secret.Name,
					"error", err,
				)
			}
			continue
		}

		r.logger.InfoContext(ctx, "deleted orphaned task secret",
			"secret", secret.Name,
			"task_run_id", secret.Labels[jobbuilder.LabelTaskRunID],
			"age", time.Since(secret.CreationTimestamp.Time).String(),
		)
	}
}
