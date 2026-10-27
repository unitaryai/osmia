package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

// launchSpec captures the inputs that genuinely differ between
// ProcessTicket and ProcessIncidentEvent for the launch tail that
// launchTaskRun runs. Everything upstream of the tail — gates,
// repo-URL resolution, engine selection, memory queries, and per-flow
// EngineConfig overrides — is built by the caller and handed in
// already formed; launchTaskRun does not re-derive any of it.
type launchSpec struct {
	// TaskRun is the already-constructed TaskRun for this launch (see
	// newLaunchTaskRun), already persisted once by its constructor.
	TaskRun *taskrun.TaskRun

	// IdempotencyKey is the map key under r.taskRuns / r.engineChains.
	IdempotencyKey string

	// EngineName is the resolved engine name that will run this task,
	// used for jobBuilder.Build, the engineChains bookkeeping, the
	// engineEmitsStream gate, and log fields.
	EngineName string

	// Engine is the resolved engine.ExecutionEngine instance for
	// EngineName. ProcessTicket and ProcessIncidentEvent already
	// resolve and validate it earlier in their own flows, so
	// launchTaskRun does not repeat that lookup.
	Engine engine.ExecutionEngine

	// EngineChain is the full fallback chain recorded against
	// IdempotencyKey in r.engineChains. Ticketing records the real,
	// possibly multi-engine chain returned by EngineSelector; incident
	// triage has no fallback selector and records a single-engine
	// chain of just EngineName.
	EngineChain []string

	// Task is the fully-built engine.Task, including TaskRunID already
	// set to TaskRun.ID.
	Task engine.Task

	// EngineConfig is the fully-built, per-flow-overridden engine
	// config.
	EngineConfig engine.EngineConfig

	// OnLaunched, if set, runs after metrics are recorded and before
	// the stream-reader gate — the exact position of
	// ticketing.MarkInProgress in the pre-refactor ProcessTicket.
	// Ticketing supplies a closure that calls MarkInProgress and logs
	// (but does not return) any error; incident triage has no
	// equivalent step and leaves this nil.
	OnLaunched func(ctx context.Context)

	// LogMessage and LogFields describe the per-flow "job created" log
	// line emitted at the end of the tail. LogFields supplies the
	// divergent leading fields (for example ticket_id, or incident_id
	// plus event_type); launchTaskRun appends the shared
	// engine/job/task_run_id fields after them.
	LogMessage string
	LogFields  []any
}

// newLaunchTaskRun constructs a new TaskRun for either flow's initial
// dispatch — wiring CurrentEngine, EngineAttempts, UseCase, and
// continuation config — and persists it immediately, before any
// flow-specific gate (for example ticketing's pre-start approval gate) or
// per-flow EngineConfig override runs. Save failures are logged, not
// returned, matching both flows' original behaviour: a task run that
// fails to save here is still launched.
//
// useCase is the registered internal/usecase Definition name for this
// flow (usecase.NameTicketing or usecase.NameIncidentTriage). Tagging
// happens here, at creation, rather than in launchTaskRun, so that a
// TaskRun held at a pre-launch gate (for example ticketing's pre-start
// approval gate) is still tagged even if it never reaches launchTaskRun
// on this call.
func (r *Reconciler) newLaunchTaskRun(ctx context.Context, id, idempotencyKey, ticketID, engineName, useCase string) *taskrun.TaskRun {
	tr := taskrun.New(id, idempotencyKey, ticketID, engineName)
	tr.CurrentEngine = engineName
	tr.EngineAttempts = []string{engineName}
	tagTaskRun(tr, useCase)
	r.applyContinuationConfig(tr)

	r.saveTaskRunOrLog(ctx, tr)

	return tr
}

// tagTaskRun stamps the use case and its episodic-memory tenant onto a
// TaskRun.
//
// The two fields track each other one-for-one today but stay separate,
// because they answer different questions: UseCase selects behaviour, while
// TenantID scopes stored knowledge, and a future use case could reasonably
// want its own behaviour while sharing another's memory. Keeping the mapping
// in one function means the pair cannot be set inconsistently.
//
// Most TaskRuns get this via newLaunchTaskRun. The ones that do not are the
// paths that build a TaskRun directly rather than going through the shared
// launch pipeline: review follow-ups, tournament candidates, the tournament
// judge, and the repo-URL poll. They must call this themselves. An untagged
// TaskRun writes its extracted facts under no tenant, where a later tenanted
// query cannot see them, and nothing fails at the time.
func tagTaskRun(tr *taskrun.TaskRun, useCase string) {
	tr.UseCase = useCase
	tr.TenantID = useCase
}

// saveTaskRunOrLog persists tr to the task run store, logging (but not
// returning) any error. Both the initial save in newLaunchTaskRun and
// the re-save in launchTaskRun, after the TaskRun transitions to
// Running, use this so a store failure never aborts a launch that has
// already created a Kubernetes Job.
func (r *Reconciler) saveTaskRunOrLog(ctx context.Context, tr *taskrun.TaskRun) {
	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store",
			"task_run_id", tr.ID,
			"error", err,
		)
	}
}

// launchTaskRun runs the task-launch tail shared by ProcessTicket and
// ProcessIncidentEvent: prepare session storage, build the execution
// spec, build and create the Kubernetes Job, transition the TaskRun to
// Running, record it in the reconciler's in-memory maps, persist it
// again, update metrics, run any flow-specific post-launch hook, start
// the stream reader if the engine emits one, and log completion.
//
// Everything before this point in either flow — gates, repo-URL
// resolution, engine selection, memory queries, per-flow EngineConfig
// overrides — has already run by the time a caller builds a
// launchSpec; spec.Task and spec.EngineConfig arrive fully formed.
func (r *Reconciler) launchTaskRun(ctx context.Context, spec launchSpec) (*taskrun.TaskRun, error) {
	tr := spec.TaskRun
	tr.TicketTitle = spec.Task.Title
	tr.TicketDescription = spec.Task.Description
	tr.RepoURL = spec.Task.RepoURL

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		return nil, fmt.Errorf("preparing session storage: %w", err)
	}

	execSpec, err := spec.Engine.BuildExecutionSpec(spec.Task, spec.EngineConfig)
	if err != nil {
		return nil, fmt.Errorf("building execution spec: %w", err)
	}

	// Task-scoped secrets are resolved after the engine has built its spec,
	// so that a task cannot shadow an environment variable the engine sets
	// for itself. Resolution is fail-closed: a policy violation aborts the
	// launch rather than starting the agent without the secret.
	secretPlan, err := r.resolveTaskSecrets(ctx, spec.Task)
	if err != nil {
		return nil, err
	}
	if err := r.createTaskSecret(ctx, tr.ID, spec.EngineName, secretPlan); err != nil {
		return nil, err
	}
	if err := applyTaskSecrets(execSpec, secretPlan, taskSecretName(tr.ID)); err != nil {
		r.discardTaskSecret(ctx, tr.ID, secretPlan)
		return nil, err
	}

	if r.jobBuilder == nil {
		r.discardTaskSecret(ctx, tr.ID, secretPlan)
		return nil, fmt.Errorf("no job builder configured")
	}

	job, err := r.jobBuilder.Build(tr.ID, spec.EngineName, execSpec)
	if err != nil {
		r.discardTaskSecret(ctx, tr.ID, secretPlan)
		return nil, fmt.Errorf("building k8s job: %w", err)
	}

	if r.k8sClient != nil {
		created, err := r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			r.discardTaskSecret(ctx, tr.ID, secretPlan)
			return nil, fmt.Errorf("creating k8s job: %w", err)
		}
		// Hand ownership of the ephemeral secret to the Job so that
		// Kubernetes garbage-collects it alongside the run. An orphaned
		// secret is untidy, not fatal, so this only logs on failure.
		if err := r.adoptTaskSecret(ctx, tr.ID, created, secretPlan); err != nil {
			r.logger.WarnContext(ctx, "failed to set job as owner of task secret",
				"task_run_id", tr.ID,
				"error", err,
			)
		}
	}

	if err := tr.Transition(taskrun.StateRunning); err != nil {
		return nil, fmt.Errorf("transitioning task run: %w", err)
	}
	tr.JobName = job.Name

	r.mu.Lock()
	r.taskRuns[spec.IdempotencyKey] = tr
	r.engineChains[spec.IdempotencyKey] = spec.EngineChain
	r.mu.Unlock()

	r.saveTaskRunOrLog(ctx, tr)

	metrics.ActiveJobs.Inc()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()

	if spec.OnLaunched != nil {
		spec.OnLaunched(ctx)
	}

	if r.engineEmitsStream(spec.EngineName) {
		r.startStreamReader(ctx, tr)
	}

	fields := make([]any, 0, len(spec.LogFields)+6)
	fields = append(fields, spec.LogFields...)
	fields = append(fields, "engine", spec.EngineName, "job", job.Name, "task_run_id", tr.ID)
	r.logger.InfoContext(ctx, spec.LogMessage, fields...)

	return tr, nil
}
