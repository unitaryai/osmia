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

	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/metrics"
	"github.com/unitaryai/robodev/internal/taskrun"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/notifications"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// JobBuilder translates an ExecutionSpec into a Kubernetes Job.
type JobBuilder interface {
	Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error)
}

// Reconciler orchestrates the full TaskRun lifecycle: polling tickets,
// creating jobs, monitoring progress, and handling completion.
type Reconciler struct {
	config     *config.Config
	logger     *slog.Logger
	k8sClient  kubernetes.Interface
	ticketing  ticketing.Backend
	engines    map[string]engine.ExecutionEngine
	notifiers  []notifications.Channel
	jobBuilder JobBuilder
	namespace  string

	mu       sync.RWMutex
	taskRuns map[string]*taskrun.TaskRun // keyed by idempotency key
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

// NewReconciler creates a new Reconciler with the given configuration.
func NewReconciler(cfg *config.Config, logger *slog.Logger, opts ...ReconcilerOption) *Reconciler {
	r := &Reconciler{
		config:   cfg,
		logger:   logger,
		engines:  make(map[string]engine.ExecutionEngine),
		taskRuns: make(map[string]*taskrun.TaskRun),
	}
	for _, opt := range opts {
		opt(r)
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

	if len(tickets) == 0 {
		return nil
	}

	r.logger.InfoContext(ctx, "polled ready tickets", "count", len(tickets))

	for _, ticket := range tickets {
		if activeCount >= maxConcurrent {
			break
		}

		if err := r.processTicket(ctx, ticket); err != nil {
			r.logger.ErrorContext(ctx, "failed to process ticket",
				"ticket_id", ticket.ID,
				"error", err,
			)
			continue
		}
		activeCount++
	}

	// Check status of running jobs.
	r.checkRunningJobs(ctx)

	return nil
}

// processTicket handles a single ticket: validates guard rails, creates
// a TaskRun with idempotency, and launches a K8s Job.
func (r *Reconciler) processTicket(ctx context.Context, ticket ticketing.Ticket) error {
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

	// Validate guard rails.
	if err := r.validateGuardRails(ticket); err != nil {
		r.logger.WarnContext(ctx, "ticket rejected by guard rails",
			"ticket_id", ticket.ID,
			"reason", err,
		)
		return r.ticketing.MarkFailed(ctx, ticket.ID, fmt.Sprintf("guard rail violation: %v", err))
	}

	// Select engine.
	engineName := r.config.Engines.Default
	if engineName == "" {
		engineName = "claude-code"
	}

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

	// Build execution spec.
	task := engine.Task{
		ID:          ticket.ID,
		TicketID:    ticket.ID,
		Title:       ticket.Title,
		Description: ticket.Description,
		RepoURL:     ticket.RepoURL,
		Labels:      ticket.Labels,
	}

	engineCfg := engine.EngineConfig{
		TimeoutSeconds: r.config.GuardRails.MaxJobDurationMinutes * 60,
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

	// Store the TaskRun.
	r.mu.Lock()
	r.taskRuns[idempotencyKey] = tr
	r.mu.Unlock()

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

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()
	metrics.TaskRunDurationSeconds.WithLabelValues(tr.Engine).Observe(
		time.Since(tr.CreatedAt).Seconds(),
	)

	result := engine.TaskResult{
		Success: true,
		Summary: "task completed successfully",
	}
	tr.Result = &result

	if r.ticketing != nil {
		if err := r.ticketing.MarkComplete(ctx, tr.TicketID, result); err != nil {
			r.logger.ErrorContext(ctx, "failed to mark ticket complete",
				"ticket_id", tr.TicketID,
				"error", err,
			)
		}
	}

	r.logger.InfoContext(ctx, "task run succeeded",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"duration", time.Since(tr.CreatedAt),
	)
}

// handleJobFailed processes a failed job, retrying if allowed.
func (r *Reconciler) handleJobFailed(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.mu.Lock()

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

	r.logger.InfoContext(ctx, "task run failed",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"reason", reason,
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
