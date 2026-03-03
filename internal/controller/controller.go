// Package controller implements the main reconciliation loop for the RoboDev
// operator. It polls the ticketing backend for ready tickets, creates TaskRuns,
// launches K8s Jobs via the JobBuilder, and monitors job completion.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/unitaryai/robodev/internal/agentstream"
	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/jobbuilder"
	"github.com/unitaryai/robodev/internal/memory"
	"github.com/unitaryai/robodev/internal/metrics"
	"github.com/unitaryai/robodev/internal/prm"
	"github.com/unitaryai/robodev/internal/secretresolver"
	"github.com/unitaryai/robodev/internal/taskrun"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/approval"
	"github.com/unitaryai/robodev/pkg/plugin/notifications"
	"github.com/unitaryai/robodev/pkg/plugin/review"
	"github.com/unitaryai/robodev/pkg/plugin/scm"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// JobBuilder translates an ExecutionSpec into a Kubernetes Job.
type JobBuilder interface {
	Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error)
}

// Reconciler orchestrates the full TaskRun lifecycle: polling tickets,
// creating jobs, monitoring progress, and handling completion.
type Reconciler struct {
	config         *config.Config
	logger         *slog.Logger
	k8sClient      kubernetes.Interface
	ticketing      ticketing.Backend
	engines        map[string]engine.ExecutionEngine
	notifiers      []notifications.Channel
	jobBuilder     JobBuilder
	engineSelector EngineSelector
	taskRunStore   taskrun.TaskRunStore
	namespace      string

	mu       sync.RWMutex
	taskRuns map[string]*taskrun.TaskRun // keyed by idempotency key

	// engineChains tracks the full ordered engine list per idempotency key,
	// as determined at ticket processing time by the EngineSelector.
	engineChains map[string][]string

	// streamReaders tracks cancel functions for active stream readers,
	// keyed by task run ID. Used to stop streaming when a job completes or fails.
	streamReaders map[string]context.CancelFunc

	// ticketCache caches the full Ticket for each processed ticket ID so that
	// completion handlers (NotifyComplete, etc.) can access ticket metadata
	// (e.g. title) without an additional backend round-trip.
	ticketCache map[string]ticketing.Ticket

	// PRM (Process Reward Model) fields.
	prmConfig     prm.Config
	prmEvaluators map[string]*prm.Evaluator // keyed by task run ID

	// Memory (cross-task episodic knowledge) fields.
	memoryGraph     *memory.Graph
	memoryExtractor *memory.Extractor
	memoryQuery     *memory.QueryEngine

	// Plugin backends — stored for use by lifecycle hooks and quality gates.
	approvalBackend approval.Backend
	scmBackend      scm.Backend
	reviewBackend   review.Backend
	secretsResolver *secretresolver.Resolver
}

// ReconcilerOption configures the Reconciler.
type ReconcilerOption func(*Reconciler)

// WithTicketing sets the ticketing backend.
func WithTicketing(t ticketing.Backend) ReconcilerOption {
	return func(r *Reconciler) { r.ticketing = t }
}

// WithEngine registers an execution engine.
func WithEngine(e engine.ExecutionEngine) ReconcilerOption {
	return func(r *Reconciler) { r.engines[e.Name()] = e }
}

// WithNotifier adds a notification channel.
func WithNotifier(n notifications.Channel) ReconcilerOption {
	return func(r *Reconciler) { r.notifiers = append(r.notifiers, n) }
}

// WithJobBuilder sets the job builder.
func WithJobBuilder(jb JobBuilder) ReconcilerOption {
	return func(r *Reconciler) { r.jobBuilder = jb }
}

// WithK8sClient sets the Kubernetes client.
func WithK8sClient(c kubernetes.Interface) ReconcilerOption {
	return func(r *Reconciler) { r.k8sClient = c }
}

// WithNamespace sets the namespace for job creation.
func WithNamespace(ns string) ReconcilerOption {
	return func(r *Reconciler) { r.namespace = ns }
}

// WithEngineSelector sets a custom engine selector for fallback chain logic.
func WithEngineSelector(es EngineSelector) ReconcilerOption {
	return func(r *Reconciler) { r.engineSelector = es }
}

// WithTaskRunStore sets a persistent TaskRun store.
func WithTaskRunStore(s taskrun.TaskRunStore) ReconcilerOption {
	return func(r *Reconciler) { r.taskRunStore = s }
}

// WithPRMConfig sets the Process Reward Model configuration. When the
// config has Enabled=true, the controller scores agent tool calls in
// real-time and produces interventions (nudge hints or escalation).
func WithPRMConfig(cfg prm.Config) ReconcilerOption {
	return func(r *Reconciler) { r.prmConfig = cfg }
}

// WithMemory sets the episodic memory subsystem components. When all three
// are non-nil, the controller extracts knowledge from completed tasks and
// injects relevant prior knowledge into new task prompts.
func WithMemory(g *memory.Graph, e *memory.Extractor, q *memory.QueryEngine) ReconcilerOption {
	return func(r *Reconciler) {
		r.memoryGraph = g
		r.memoryExtractor = e
		r.memoryQuery = q
	}
}

// WithApprovalBackend sets the human approval backend. When non-nil, the
// controller uses it to deliver approval gate questions to humans (e.g. via
// Slack) and to cancel pending requests when a TaskRun is terminated.
func WithApprovalBackend(b approval.Backend) ReconcilerOption {
	return func(r *Reconciler) { r.approvalBackend = b }
}

// WithSCMBackend sets the source code management backend. When non-nil, the
// controller can create branches and open pull/merge requests on behalf of
// the agent.
func WithSCMBackend(b scm.Backend) ReconcilerOption {
	return func(r *Reconciler) { r.scmBackend = b }
}

// WithReviewBackend sets the code review backend. When non-nil, the quality
// gate submits agent diffs for automated review before finalising a TaskRun.
func WithReviewBackend(b review.Backend) ReconcilerOption {
	return func(r *Reconciler) { r.reviewBackend = b }
}

// WithSecretsResolver sets the task-scoped secrets resolver. When non-nil,
// the controller resolves secret references from ticket descriptions and
// labels before injecting them into the agent's execution environment.
func WithSecretsResolver(sr *secretresolver.Resolver) ReconcilerOption {
	return func(r *Reconciler) { r.secretsResolver = sr }
}

// NewReconciler creates a new Reconciler with the given configuration.
func NewReconciler(cfg *config.Config, logger *slog.Logger, opts ...ReconcilerOption) *Reconciler {
	r := &Reconciler{
		config:        cfg,
		logger:        logger,
		engines:       make(map[string]engine.ExecutionEngine),
		taskRuns:      make(map[string]*taskrun.TaskRun),
		engineChains:  make(map[string][]string),
		streamReaders: make(map[string]context.CancelFunc),
		prmEvaluators: make(map[string]*prm.Evaluator),
		ticketCache:   make(map[string]ticketing.Ticket),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.engineSelector == nil {
		r.engineSelector = NewDefaultEngineSelector(cfg, r.engines)
	}
	if r.taskRunStore == nil {
		r.taskRunStore = taskrun.NewMemoryStore()
	}
	return r
}

// Run starts the main reconciliation loop. It polls the ticketing backend
// at a regular interval and reconciles each discovered ticket. The loop
// runs until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context, pollInterval time.Duration) error {
	r.logger.InfoContext(ctx, "starting reconciliation loop",
		"poll_interval", pollInterval,
		"namespace", r.namespace,
	)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.InfoContext(ctx, "reconciliation loop stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := r.reconcileOnce(ctx); err != nil {
				r.logger.ErrorContext(ctx, "reconciliation error", "error", err)
			}
		}
	}
}

// reconcileOnce performs a single reconciliation cycle: poll for tickets,
// check guard rails, create task runs, launch jobs, and check job statuses.
func (r *Reconciler) reconcileOnce(ctx context.Context) error {
	// Check concurrent job limit.
	activeCount := r.activeJobCount()
	maxConcurrent := r.config.GuardRails.MaxConcurrentJobs
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}

	if activeCount >= maxConcurrent {
		r.logger.InfoContext(ctx, "at concurrent job limit, skipping poll",
			"active", activeCount,
			"max", maxConcurrent,
		)
		return nil
	}

	// Poll for ready tickets.
	if r.ticketing == nil {
		return fmt.Errorf("no ticketing backend configured")
	}

	tickets, err := r.ticketing.PollReadyTickets(ctx)
	if err != nil {
		return fmt.Errorf("polling tickets: %w", err)
	}

	// Always check running job status, regardless of whether new tickets arrived.
	// Previously this was only called when tickets > 0, which caused completed
	// jobs to go undetected until the next ticket was ready.
	defer r.checkRunningJobs(ctx)

	if len(tickets) == 0 {
		return nil
	}

	r.logger.InfoContext(ctx, "polled ready tickets", "count", len(tickets))

	for _, ticket := range tickets {
		if activeCount >= maxConcurrent {
			break
		}

		if err := r.ProcessTicket(ctx, ticket); err != nil {
			r.logger.ErrorContext(ctx, "failed to process ticket",
				"ticket_id", ticket.ID,
				"error", err,
			)
			continue
		}
		activeCount++
	}

	return nil
}

// ProcessTicket handles a single ticket: validates guard rails, creates
// a TaskRun with idempotency, and launches a K8s Job. It is exported so
// that the webhook server adapter can feed tickets into the reconciler.
func (r *Reconciler) ProcessTicket(ctx context.Context, ticket ticketing.Ticket) error {
	// Generate idempotency key.
	idempotencyKey := fmt.Sprintf("%s-1", ticket.ID)

	// Check for existing TaskRun (idempotency).
	r.mu.RLock()
	if existing, ok := r.taskRuns[idempotencyKey]; ok {
		r.mu.RUnlock()
		if !existing.IsTerminal() {
			r.logger.InfoContext(ctx, "task run already exists, skipping",
				"ticket_id", ticket.ID,
				"state", existing.State,
			)
			return nil
		}
	} else {
		r.mu.RUnlock()
	}

	// Cache the ticket so completion handlers can access its metadata.
	r.mu.Lock()
	r.ticketCache[ticket.ID] = ticket
	r.mu.Unlock()

	// Validate guard rails.
	if err := r.validateGuardRails(ticket); err != nil {
		r.logger.WarnContext(ctx, "ticket rejected by guard rails",
			"ticket_id", ticket.ID,
			"reason", err,
		)
		return r.ticketing.MarkFailed(ctx, ticket.ID, fmt.Sprintf("guard rail violation: %v", err))
	}

	// Select engines using the configured selector (returns ordered fallback chain).
	engineChain := r.engineSelector.SelectEngines(ticket)
	if len(engineChain) == 0 {
		return fmt.Errorf("no registered engines available for ticket %q", ticket.ID)
	}

	engineName := engineChain[0]
	eng, ok := r.engines[engineName]
	if !ok {
		return fmt.Errorf("engine %q not registered", engineName)
	}

	// Create TaskRun.
	tr := taskrun.New(
		fmt.Sprintf("tr-%s-%d", ticket.ID, time.Now().UnixMilli()),
		idempotencyKey,
		ticket.ID,
		engineName,
	)
	tr.CurrentEngine = engineName
	tr.EngineAttempts = []string{engineName}

	// Persist the newly created TaskRun.
	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	// Check pre-start approval gate: if configured, hold the TaskRun in
	// NeedsHuman state instead of launching a job immediately.
	if r.hasApprovalGate("pre_start") {
		if err := tr.Transition(taskrun.StateNeedsHuman); err != nil {
			return fmt.Errorf("transitioning task run to needs human: %w", err)
		}
		tr.HumanQuestion = "approve task start?"

		r.mu.Lock()
		r.taskRuns[idempotencyKey] = tr
		r.engineChains[idempotencyKey] = engineChain
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run to store after approval gate",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateNeedsHuman)).Inc()

		r.logger.InfoContext(ctx, "task run held for pre-start approval",
			"ticket_id", ticket.ID,
			"task_run_id", tr.ID,
		)
		return nil
	}

	// Query episodic memory for prior knowledge relevant to this task.
	var memoryContext string
	if r.memoryQuery != nil {
		mc, qErr := r.memoryQuery.QueryForTask(ctx, ticket.Description, ticket.RepoURL, engineName, "")
		if qErr != nil {
			r.logger.WarnContext(ctx, "memory query failed, continuing without prior knowledge",
				"ticket_id", ticket.ID,
				"error", qErr,
			)
		} else if mc != nil && mc.FormattedSection != "" {
			memoryContext = mc.FormattedSection
		}
	}

	// Build execution spec.
	task := engine.Task{
		ID:            ticket.ID,
		TicketID:      ticket.ID,
		Title:         ticket.Title,
		Description:   ticket.Description,
		RepoURL:       ticket.RepoURL,
		Labels:        ticket.Labels,
		MemoryContext: memoryContext,
	}

	engineCfg := engine.EngineConfig{
		TimeoutSeconds: r.config.GuardRails.MaxJobDurationMinutes * 60,
		Image:          r.config.Engines.ImageFor(engineName),
		SecretKeyRefs:  r.agentSecretKeyRefs(),
		Env:            r.slackEnv(),
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		return fmt.Errorf("building execution spec: %w", err)
	}

	// Build K8s Job.
	if r.jobBuilder == nil {
		return fmt.Errorf("no job builder configured")
	}

	job, err := r.jobBuilder.Build(tr.ID, engineName, spec)
	if err != nil {
		return fmt.Errorf("building k8s job: %w", err)
	}

	// Create the job in Kubernetes.
	if r.k8sClient != nil {
		_, err = r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating k8s job: %w", err)
		}
	}

	// Transition to Running.
	if err := tr.Transition(taskrun.StateRunning); err != nil {
		return fmt.Errorf("transitioning task run: %w", err)
	}

	tr.JobName = job.Name

	// Store the TaskRun and engine chain.
	r.mu.Lock()
	r.taskRuns[idempotencyKey] = tr
	r.engineChains[idempotencyKey] = engineChain
	r.mu.Unlock()

	// Persist state after transition.
	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	// Update metrics.
	metrics.ActiveJobs.Inc()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()

	// Mark ticket as in progress.
	if err := r.ticketing.MarkInProgress(ctx, ticket.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to mark ticket in progress",
			"ticket_id", ticket.ID,
			"error", err,
		)
	}

	// Send notifications.
	for _, n := range r.notifiers {
		if err := n.NotifyStart(ctx, ticket); err != nil {
			r.logger.ErrorContext(ctx, "notification failed",
				"channel", n.Name(),
				"error", err,
			)
		}
	}

	// Start stream reader for claude-code to parse NDJSON events from pod logs.
	if engineName == "claude-code" {
		r.startStreamReader(ctx, tr)
	}

	r.logger.InfoContext(ctx, "job created",
		"ticket_id", ticket.ID,
		"engine", engineName,
		"job", job.Name,
		"task_run_id", tr.ID,
	)

	return nil
}

// validateGuardRails checks whether a ticket passes controller-level guard rails.
func (r *Reconciler) validateGuardRails(ticket ticketing.Ticket) error {
	gr := r.config.GuardRails

	// Check allowed repositories.
	if len(gr.AllowedRepos) > 0 && ticket.RepoURL != "" {
		matched := false
		for _, pattern := range gr.AllowedRepos {
			if matchGlob(pattern, ticket.RepoURL) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("repository %q not in allowed list", ticket.RepoURL)
		}
	}

	// Check allowed task types.
	if len(gr.AllowedTaskTypes) > 0 && ticket.TicketType != "" {
		allowed := false
		for _, t := range gr.AllowedTaskTypes {
			if t == ticket.TicketType {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("task type %q not allowed", ticket.TicketType)
		}
	}

	return nil
}

// checkRunningJobs inspects all active TaskRuns and updates their state
// based on the corresponding K8s Job status.
func (r *Reconciler) checkRunningJobs(ctx context.Context) {
	r.mu.RLock()
	running := make([]*taskrun.TaskRun, 0)
	for _, tr := range r.taskRuns {
		if tr.State == taskrun.StateRunning {
			running = append(running, tr)
		}
	}
	r.mu.RUnlock()

	for _, tr := range running {
		r.checkJobStatus(ctx, tr)
	}
}

// checkJobStatus checks the K8s Job status for a single TaskRun and
// transitions its state accordingly.
func (r *Reconciler) checkJobStatus(ctx context.Context, tr *taskrun.TaskRun) {
	if r.k8sClient == nil || tr.JobName == "" {
		return
	}

	job, err := r.k8sClient.BatchV1().Jobs(r.namespace).Get(ctx, tr.JobName, metav1.GetOptions{})
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to get job status",
			"job", tr.JobName,
			"error", err,
		)
		return
	}

	for _, condition := range job.Status.Conditions {
		switch condition.Type {
		case batchv1.JobComplete:
			if condition.Status == corev1.ConditionTrue {
				r.handleJobComplete(ctx, tr)
				return
			}
		case batchv1.JobFailed:
			if condition.Status == corev1.ConditionTrue {
				r.handleJobFailed(ctx, tr, condition.Message)
				return
			}
		}
	}
}

// handleJobComplete processes a successfully completed job.
func (r *Reconciler) handleJobComplete(ctx context.Context, tr *taskrun.TaskRun) {
	r.cancelStreamReader(tr.ID)
	r.cleanupPRMEvaluator(tr.ID)

	// Check pre-merge approval gate: hold the TaskRun in NeedsHuman state
	// before marking the ticket complete, so a human can review the output.
	if r.hasApprovalGate("pre_merge") {
		r.mu.Lock()
		if err := tr.Transition(taskrun.StateNeedsHuman); err != nil {
			r.mu.Unlock()
			r.logger.ErrorContext(ctx, "failed to transition task run to needs human for pre-merge approval",
				"task_run_id", tr.ID,
				"error", err,
			)
			return
		}
		tr.HumanQuestion = "approve merge of completed task?"
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run to store",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		r.logger.InfoContext(ctx, "task run held for pre-merge approval",
			"task_run_id", tr.ID,
			"ticket_id", tr.TicketID,
		)
		return
	}

	r.mu.Lock()
	if err := tr.Transition(taskrun.StateSucceeded); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition task run to succeeded",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()
	metrics.TaskRunDurationSeconds.WithLabelValues(tr.Engine).Observe(
		time.Since(tr.CreatedAt).Seconds(),
	)

	// Use the result captured by the stream reader processor; fall back to a
	// generic success result if the stream reader did not capture one yet.
	r.mu.RLock()
	result := engine.TaskResult{Success: true, Summary: "task completed successfully"}
	if tr.Result != nil {
		result = *tr.Result
	}
	r.mu.RUnlock()
	tr.Result = &result

	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	if r.ticketing != nil {
		if err := r.ticketing.MarkComplete(ctx, tr.TicketID, result); err != nil {
			r.logger.ErrorContext(ctx, "failed to mark ticket complete",
				"ticket_id", tr.TicketID,
				"error", err,
			)
		}
	}

	if hasTicket {
		for _, n := range r.notifiers {
			if err := n.NotifyComplete(ctx, cachedTicket, result); err != nil {
				r.logger.ErrorContext(ctx, "completion notification failed",
					"ticket_id", tr.TicketID,
					"error", err,
				)
			}
		}
	}

	// Extract knowledge from the completed task into episodic memory.
	if r.memoryExtractor != nil {
		go r.extractMemory(ctx, tr)
	}

	r.logger.InfoContext(ctx, "task run succeeded",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"duration", time.Since(tr.CreatedAt),
	)
}

// handleJobFailed processes a failed job, attempting engine fallback or
// retrying with the same engine if allowed.
func (r *Reconciler) handleJobFailed(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.cancelStreamReader(tr.ID)
	r.cleanupPRMEvaluator(tr.ID)

	r.mu.Lock()

	// Check whether a fallback engine is available before exhausting retries.
	chain := r.engineChains[tr.IdempotencyKey]
	if len(chain) > len(tr.EngineAttempts) {
		nextEngine := chain[len(tr.EngineAttempts)]
		previousEngine := tr.CurrentEngine

		if err := tr.Transition(taskrun.StateFailed); err != nil {
			r.mu.Unlock()
			return
		}
		tr.CurrentEngine = nextEngine
		tr.EngineAttempts = append(tr.EngineAttempts, nextEngine)
		if err := tr.Transition(taskrun.StateRetrying); err != nil {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run to store",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		r.logger.InfoContext(ctx, "falling back to next engine",
			"from", previousEngine,
			"to", nextEngine,
			"task_run", tr.ID,
		)

		r.launchFallbackJob(ctx, tr, nextEngine)
		return
	}

	if tr.RetryCount < tr.MaxRetries {
		if err := tr.Transition(taskrun.StateFailed); err != nil {
			r.mu.Unlock()
			return
		}
		tr.RetryCount++
		if err := tr.Transition(taskrun.StateRetrying); err != nil {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run to store",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		r.logger.InfoContext(ctx, "retrying task run",
			"task_run_id", tr.ID,
			"retry_count", tr.RetryCount,
			"max_retries", tr.MaxRetries,
		)
		return
	}

	if err := tr.Transition(taskrun.StateFailed); err != nil {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateFailed)).Inc()

	if r.ticketing != nil {
		if err := r.ticketing.MarkFailed(ctx, tr.TicketID, reason); err != nil {
			r.logger.ErrorContext(ctx, "failed to mark ticket failed",
				"ticket_id", tr.TicketID,
				"error", err,
			)
		}
	}

	// Extract knowledge from the failed task into episodic memory.
	if r.memoryExtractor != nil {
		go r.extractMemory(ctx, tr)
	}

	r.logger.InfoContext(ctx, "task run failed",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"reason", reason,
	)
}

// launchFallbackJob builds and creates a new K8s Job using the fallback
// engine, then transitions the TaskRun back to Running.
func (r *Reconciler) launchFallbackJob(ctx context.Context, tr *taskrun.TaskRun, engineName string) {
	eng, ok := r.engines[engineName]
	if !ok {
		r.logger.ErrorContext(ctx, "fallback engine not registered",
			"engine", engineName,
			"task_run", tr.ID,
		)
		return
	}

	task := engine.Task{
		ID:       tr.TicketID,
		TicketID: tr.TicketID,
	}

	engineCfg := engine.EngineConfig{
		TimeoutSeconds: r.config.GuardRails.MaxJobDurationMinutes * 60,
		Image:          r.config.Engines.ImageFor(engineName),
		SecretKeyRefs:  r.agentSecretKeyRefs(),
		Env:            r.slackEnv(),
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build fallback execution spec",
			"engine", engineName,
			"task_run", tr.ID,
			"error", err,
		)
		return
	}

	if r.jobBuilder == nil {
		return
	}

	// Use a unique job ID that includes the attempt number to avoid K8s
	// name collisions with the previous engine's job.
	jobID := fmt.Sprintf("%s-fb%d", tr.ID, len(tr.EngineAttempts))
	job, err := r.jobBuilder.Build(jobID, engineName, spec)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build fallback k8s job",
			"engine", engineName,
			"task_run", tr.ID,
			"error", err,
		)
		return
	}

	if r.k8sClient != nil {
		_, err = r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			r.logger.ErrorContext(ctx, "failed to create fallback k8s job",
				"engine", engineName,
				"task_run", tr.ID,
				"error", err,
			)
			return
		}
	}

	r.mu.Lock()
	if err := tr.Transition(taskrun.StateRunning); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition task run to running after fallback",
			"task_run", tr.ID,
			"error", err,
		)
		return
	}
	tr.JobName = job.Name
	r.mu.Unlock()

	r.logger.InfoContext(ctx, "fallback job created",
		"engine", engineName,
		"job", job.Name,
		"task_run_id", tr.ID,
	)
}

// activeJobCount returns the number of currently active (non-terminal) TaskRuns.
func (r *Reconciler) activeJobCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, tr := range r.taskRuns {
		if !tr.IsTerminal() && tr.State != taskrun.StateQueued {
			count++
		}
	}
	return count
}

// GetTaskRun returns the TaskRun for the given idempotency key, if it exists.
func (r *Reconciler) GetTaskRun(idempotencyKey string) (*taskrun.TaskRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tr, ok := r.taskRuns[idempotencyKey]
	return tr, ok
}

// startStreamReader launches a background goroutine that reads NDJSON events
// from the agent pod's logs and forwards them through the event pipeline.
func (r *Reconciler) startStreamReader(ctx context.Context, tr *taskrun.TaskRun) {
	streamCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.streamReaders[tr.ID] = cancel
	r.mu.Unlock()

	go func() {
		eventCh := make(chan *agentstream.StreamEvent, 100)

		reader := agentstream.NewReader(r.logger.With("component", "stream-reader", "task_run_id", tr.ID))

		var forwarderOpts []agentstream.ForwarderOption

		// Capture the final result event so handleJobComplete can use it.
		resultProcessor := func(_ context.Context, event *agentstream.StreamEvent) {
			if event.Type != agentstream.EventResult {
				return
			}
			re, ok := event.Parsed.(*agentstream.ResultEvent)
			if !ok || re == nil {
				return
			}
			r.mu.Lock()
			tr.Result = &engine.TaskResult{
				Success:         re.Success,
				Summary:         re.Summary,
				MergeRequestURL: re.MergeRequestURL,
				BranchName:      re.BranchName,
			}
			r.mu.Unlock()
		}
		forwarderOpts = append(forwarderOpts, agentstream.WithEventProcessor(resultProcessor))

		// Wire PRM evaluator into the streaming pipeline when enabled.
		if r.prmConfig.Enabled {
			evaluator := prm.NewEvaluator(r.prmConfig, r.logger.With("component", "prm", "task_run_id", tr.ID))
			r.mu.Lock()
			r.prmEvaluators[tr.ID] = evaluator
			r.mu.Unlock()

			prmProcessor := func(ctx context.Context, event *agentstream.StreamEvent) {
				intervention := evaluator.ProcessEvent(ctx, event)
				if intervention == nil || intervention.Action == prm.ActionContinue {
					return
				}

				switch intervention.Action {
				case prm.ActionNudge:
					r.recordPRMHint(ctx, tr, intervention)
				case prm.ActionEscalate:
					r.logger.WarnContext(ctx, "prm escalation triggered, deferring to watchdog",
						"task_run_id", tr.ID,
						"reason", intervention.Reason,
					)
				}
			}
			forwarderOpts = append(forwarderOpts, agentstream.WithEventProcessor(prmProcessor))
		}

		forwarder := agentstream.NewForwarder(
			r.logger.With("component", "stream-forwarder", "task_run_id", tr.ID),
			forwarderOpts...,
		)

		// Start the log reader in a separate goroutine.
		go func() {
			defer close(eventCh)
			if r.k8sClient == nil {
				return
			}

			// Resolve the actual pod name. K8s Job pods are named
			// <jobname>-<random-suffix>, so we cannot use tr.JobName directly.
			podName, err := r.resolvePodName(streamCtx, tr.ID)
			if err != nil {
				if streamCtx.Err() == nil {
					r.logger.Warn("failed to resolve pod name for stream reader",
						"task_run_id", tr.ID,
						"error", err,
					)
				}
				return
			}

			r.logger.Info("stream reader resolved pod",
				"task_run_id", tr.ID,
				"pod", podName,
			)

			if err := reader.ReadPodLogs(streamCtx, r.k8sClient, r.namespace, podName, "agent", eventCh); err != nil {
				if streamCtx.Err() == nil {
					r.logger.Warn("stream reader stopped with error",
						"task_run_id", tr.ID,
						"pod", podName,
						"error", err,
					)
				}
			}
		}()

		// Forward events until the channel is closed or context is cancelled.
		if err := forwarder.Forward(streamCtx, eventCh); err != nil {
			if streamCtx.Err() == nil {
				r.logger.Warn("stream forwarder stopped with error",
					"task_run_id", tr.ID,
					"error", err,
				)
			}
		}
	}()

	r.logger.Info("stream reader started",
		"task_run_id", tr.ID,
		"job", tr.JobName,
	)
}

// resolvePodName looks up the name of the pod created by the K8s Job for the
// given task run ID. It polls until the agent container has started (i.e., is
// Running or Terminated) or the context is cancelled, using exponential backoff
// starting at 2 s and capped at 30 s. Waiting for the container to start
// avoids a "ContainerCreating" error when the log stream is opened.
func (r *Reconciler) resolvePodName(ctx context.Context, taskRunID string) (string, error) {
	labelSelector := fmt.Sprintf("%s=%s", jobbuilder.LabelTaskRunID, taskRunID)
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		pods, err := r.k8sClient.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return "", fmt.Errorf("listing pods for task run %q: %w", taskRunID, err)
		}

		for i := range pods.Items {
			pod := &pods.Items[i]
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name == "agent" && (cs.State.Running != nil || cs.State.Terminated != nil) {
					return pod.Name, nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}
}

// cancelStreamReader stops the stream reader goroutine for the given task run
// and removes it from the map. It is safe to call even if no reader exists.
func (r *Reconciler) cancelStreamReader(taskRunID string) {
	r.mu.Lock()
	cancel, ok := r.streamReaders[taskRunID]
	if ok {
		delete(r.streamReaders, taskRunID)
	}
	r.mu.Unlock()

	if ok {
		cancel()
		r.logger.Debug("stream reader cancelled", "task_run_id", taskRunID)
	}
}

// recordPRMHint logs the PRM intervention and records it on the TaskRun so
// that the quality gate and audit trail have visibility. In v1 we do not
// write directly to the agent pod; a future iteration will deliver hints
// via a projected ConfigMap volume.
func (r *Reconciler) recordPRMHint(ctx context.Context, tr *taskrun.TaskRun, intervention *prm.Intervention) {
	r.logger.InfoContext(ctx, "prm nudge recorded",
		"task_run_id", tr.ID,
		"reason", intervention.Reason,
		"hint_length", len(intervention.HintContent),
	)

	metrics.PRMInterventionsTotal.WithLabelValues(string(intervention.Action)).Inc()
}

// cleanupPRMEvaluator removes the PRM evaluator for the given task run ID.
// It is safe to call even if no evaluator exists.
func (r *Reconciler) cleanupPRMEvaluator(taskRunID string) {
	r.mu.Lock()
	delete(r.prmEvaluators, taskRunID)
	r.mu.Unlock()
}

// extractMemory performs post-task knowledge extraction from a completed or
// failed TaskRun. It runs in a background goroutine and logs errors without
// affecting the TaskRun outcome.
func (r *Reconciler) extractMemory(ctx context.Context, tr *taskrun.TaskRun) {
	nodes, edges, err := r.memoryExtractor.Extract(ctx, tr, nil)
	if err != nil {
		r.logger.WarnContext(ctx, "memory extraction failed",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	for _, node := range nodes {
		if err := r.memoryGraph.AddNode(ctx, node); err != nil {
			r.logger.WarnContext(ctx, "failed to add memory node",
				"task_run_id", tr.ID,
				"node_id", node.NodeID(),
				"error", err,
			)
		}
	}

	for _, edge := range edges {
		if err := r.memoryGraph.AddEdge(ctx, edge); err != nil {
			r.logger.WarnContext(ctx, "failed to add memory edge",
				"task_run_id", tr.ID,
				"error", err,
			)
		}
	}

	r.logger.InfoContext(ctx, "memory extraction completed",
		"task_run_id", tr.ID,
		"nodes_extracted", len(nodes),
		"edges_extracted", len(edges),
	)
}

// slackSecretKeyRefs returns a SecretKeyRef for the Slack bot token read from
// the first configured Slack notification channel. Returns nil when no Slack
// channel is configured or the token secret is absent.
func (r *Reconciler) slackSecretKeyRefs() map[string]engine.SecretKeyRef {
	for _, ch := range r.config.Notifications.Channels {
		if ch.Backend != "slack" {
			continue
		}
		tokenSecret, _ := ch.Config["token_secret"].(string)
		if tokenSecret == "" {
			continue
		}
		return map[string]engine.SecretKeyRef{
			"SLACK_BOT_TOKEN": {SecretName: tokenSecret, Key: "token"},
		}
	}
	return nil
}

// slackEnv returns env var entries for the Slack MCP server. It reads the
// channel ID from the first configured Slack notification channel. Returns nil
// when no Slack channel is configured.
func (r *Reconciler) slackEnv() map[string]string {
	for _, ch := range r.config.Notifications.Channels {
		if ch.Backend != "slack" {
			continue
		}
		channelID, _ := ch.Config["channel_id"].(string)
		if channelID == "" {
			continue
		}
		return map[string]string{"SLACK_CHANNEL_ID": channelID}
	}
	return nil
}

// agentSecretKeyRefs returns all SecretKeyRef entries for agent pods,
// combining SCM and Slack credentials.
func (r *Reconciler) agentSecretKeyRefs() map[string]engine.SecretKeyRef {
	refs := make(map[string]engine.SecretKeyRef)
	for k, v := range r.scmSecretKeyRefs() {
		refs[k] = v
	}
	for k, v := range r.slackSecretKeyRefs() {
		refs[k] = v
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// scmSecretKeyRefs returns SecretKeyRef entries for the configured SCM token
// so that agent pods can authenticate with the SCM provider when cloning
// private repositories. Returns nil when no SCM backend is configured.
func (r *Reconciler) scmSecretKeyRefs() map[string]engine.SecretKeyRef {
	scm := r.config.SCM
	if scm.Backend == "" {
		return nil
	}
	tokenSecret, _ := scm.Config["token_secret"].(string)
	if tokenSecret == "" {
		return nil
	}
	var envVarName string
	switch scm.Backend {
	case "gitlab":
		envVarName = "GITLAB_TOKEN"
	case "github":
		envVarName = "GITHUB_TOKEN"
	default:
		return nil
	}
	return map[string]engine.SecretKeyRef{
		envVarName: {SecretName: tokenSecret, Key: "token"},
	}
}

// hasApprovalGate returns true if the given gate name is present in the
// configured approval gates list.
func (r *Reconciler) hasApprovalGate(gate string) bool {
	for _, g := range r.config.GuardRails.ApprovalGates {
		if g == gate {
			return true
		}
	}
	return false
}

// matchGlob performs a simple glob match supporting * wildcards.
func matchGlob(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(value) >= len(prefix) && value[:len(prefix)] == prefix
	}
	if len(pattern) > 0 && pattern[0] == '*' {
		suffix := pattern[1:]
		return len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix
	}
	return pattern == value
}
