// Package controller implements the main reconciliation loop for the Osmia
// operator. It polls the ticketing backend for ready tickets, creates TaskRuns,
// launches K8s Jobs via the JobBuilder, and monitors job completion.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/estimator"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/prm"
	"github.com/unitaryai/osmia/internal/reviewpoller"
	"github.com/unitaryai/osmia/internal/routing"
	"github.com/unitaryai/osmia/internal/scmrouter"
	"github.com/unitaryai/osmia/internal/secretresolver"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/internal/tournament"
	"github.com/unitaryai/osmia/internal/watchdog"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/approval"
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/review"
	"github.com/unitaryai/osmia/pkg/plugin/scm"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
	"github.com/unitaryai/osmia/pkg/plugin/transcript"
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

	// sessionStore is used to provision per-TaskRun storage (e.g. PVCs) before
	// agent jobs are created. May be nil when session persistence is disabled.
	sessionStore engine.SessionStore

	// Plugin backends — stored for use by lifecycle hooks and quality gates.
	approvalBackend approval.Backend
	scmBackend      scm.Backend
	scmRouter       *scmrouter.Router
	reviewBackend   review.Backend
	secretsResolver *secretresolver.Resolver

	// Diagnosis subsystem — classifies failures and enriches retry prompts.
	analyser     *diagnosis.Analyser
	retryBuilder *diagnosis.RetryBuilder

	// Watchdog subsystem — detects stalled, looping, or unproductive agents.
	wd              *watchdog.Watchdog
	calibrator      *watchdog.Calibrator
	profileResolver *watchdog.ProfileResolver
	heartbeats      map[string]*watchdog.Heartbeat
	heartbeatSeqs   map[string]int64

	// Intelligent routing — selects engines based on historical fingerprints.
	intelligentSelector *routing.IntelligentSelector

	// Cost/duration estimation — predicts and records task costs.
	estimatorPredictor *estimator.Predictor
	complexityScorer   *estimator.ComplexityScorer

	// Transcript storage — persists agent event streams as audit logs.
	transcriptSink transcript.TranscriptSink

	// restConfig is the Kubernetes REST config used for pod exec (hint file writes).
	restConfig *rest.Config

	// podNames maps task run IDs to the resolved pod name, populated once
	// the stream reader finds the running agent pod.
	podNames map[string]string

	// Tournament coordinator — manages competitive parallel execution.
	tournamentCoordinator *tournament.Coordinator

	// taskRunRole tracks whether a task run is a tournament "candidate" or "judge".
	// Absent entries are treated as normal (non-tournament) runs.
	taskRunRole map[string]string

	// taskRunToTournament maps a candidate or judge task run ID to its tournament ID.
	taskRunToTournament map[string]string

	// reviewPoller monitors open PRs/MRs for review comments and emits
	// follow-up task requests. Nil when review response is disabled.
	reviewPoller *reviewpoller.Poller

	// ticketNotificationRefs caches the thread reference (e.g. Slack message
	// timestamp) returned by NotifyStart for each ticket ID so that
	// NotifyComplete and subsequent Notify calls can thread under the initial
	// notification. Keyed by ticket ID; protected by mu.
	ticketNotificationRefs map[string]string

	// repoURLPoller asks humans for a repo URL when one is missing from
	// the ticket. Nil when no interactive channel is configured.
	repoURLPoller RepoURLPoller
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

// WithSCMRouter sets a multi-backend SCM router. When non-nil, the controller
// uses it to select the correct SCM backend for each task based on the
// repository URL, superseding the single scmBackend field.
func WithSCMRouter(router *scmrouter.Router) ReconcilerOption {
	return func(r *Reconciler) { r.scmRouter = router }
}

// WithReviewPoller sets the review comment poller. When non-nil, the
// controller registers newly opened PRs for monitoring and processes
// follow-up task requests on each reconciliation tick.
func WithReviewPoller(p *reviewpoller.Poller) ReconcilerOption {
	return func(r *Reconciler) { r.reviewPoller = p }
}

// WithSessionStore sets the session store used to provision per-TaskRun
// storage before agent jobs are created.
func WithSessionStore(s engine.SessionStore) ReconcilerOption {
	return func(r *Reconciler) { r.sessionStore = s }
}

// baseEngineConfig returns an EngineConfig pre-populated with the fields that
// are common across all job creation sites: timeout, image, secret refs, env,
// and the API key secret name resolved from the engine's auth config.
func (r *Reconciler) baseEngineConfig(engineName string) engine.EngineConfig {
	apiKeySecret := ""
	if r.config.Engines.ClaudeCode != nil && engineName == "claude-code" {
		apiKeySecret = r.config.Engines.ClaudeCode.Auth.APIKeySecret
	}
	return engine.EngineConfig{
		TimeoutSeconds: r.config.GuardRails.MaxJobDurationMinutes * 60,
		Image:          r.config.Engines.ImageFor(engineName),
		SecretKeyRefs:  r.agentSecretKeyRefs(),
		Env:            r.slackEnv(),
		APIKeySecret:   apiKeySecret,
	}
}

// prepareSession calls Prepare on the session store for the given TaskRun ID.
// It is a no-op when no session store is configured. Must be called before
// BuildExecutionSpec so that any required storage (e.g. a per-TaskRun PVC)
// exists before the K8s Job references it.
func (r *Reconciler) prepareSession(ctx context.Context, taskRunID string) error {
	if r.sessionStore == nil {
		return nil
	}
	return r.sessionStore.Prepare(ctx, taskRunID)
}

// WithDiagnosis enables the causal failure diagnosis subsystem. When both
// arguments are non-nil, failed tasks are analysed before retry, with an
// enriched prompt and optional engine switch based on the diagnosis.
func WithDiagnosis(analyser *diagnosis.Analyser, retryBuilder *diagnosis.RetryBuilder) ReconcilerOption {
	return func(r *Reconciler) {
		r.analyser = analyser
		r.retryBuilder = retryBuilder
	}
}

// WithWatchdog sets the progress watchdog. When non-nil, the watchdog loop
// is started inside Run and all stream events are fed through it.
func WithWatchdog(wd *watchdog.Watchdog) ReconcilerOption {
	return func(r *Reconciler) { r.wd = wd }
}

// WithWatchdogCalibration supplies the adaptive calibration components to the
// controller. When non-nil, task completion and failure events are recorded as
// calibration observations and calibrated profiles are refreshed automatically.
func WithWatchdogCalibration(cal *watchdog.Calibrator, pr *watchdog.ProfileResolver) ReconcilerOption {
	return func(r *Reconciler) {
		r.calibrator = cal
		r.profileResolver = pr
	}
}

// WithIntelligentSelector replaces the default engine selector with an
// intelligent, fingerprint-based router that learns from historical outcomes.
func WithIntelligentSelector(sel *routing.IntelligentSelector) ReconcilerOption {
	return func(r *Reconciler) { r.intelligentSelector = sel }
}

// WithEstimator enables predictive cost/duration estimation. When non-nil,
// tasks with predicted costs above the configured threshold are auto-rejected,
// and actual outcomes are fed back for future predictions.
func WithEstimator(predictor *estimator.Predictor, scorer *estimator.ComplexityScorer) ReconcilerOption {
	return func(r *Reconciler) {
		r.estimatorPredictor = predictor
		r.complexityScorer = scorer
	}
}

// WithTranscriptSink sets the audit transcript storage backend. When non-nil,
// all agent stream events are forwarded to the sink for archival.
func WithTranscriptSink(sink transcript.TranscriptSink) ReconcilerOption {
	return func(r *Reconciler) { r.transcriptSink = sink }
}

// WithRestConfig sets the Kubernetes REST config used for pod exec operations
// such as writing PRM hint files directly to the agent pod's workspace.
func WithRestConfig(cfg *rest.Config) ReconcilerOption {
	return func(r *Reconciler) { r.restConfig = cfg }
}

// WithTournamentCoordinator enables competitive execution mode. When set,
// ProcessTicket launches multiple candidate jobs in parallel and uses a
// judge engine to select the best result.
func WithTournamentCoordinator(c *tournament.Coordinator) ReconcilerOption {
	return func(r *Reconciler) { r.tournamentCoordinator = c }
}

// WithRepoURLPoller sets the poller used to ask humans for a repository
// URL when one is missing from the ticket.
func WithRepoURLPoller(p RepoURLPoller) ReconcilerOption {
	return func(r *Reconciler) { r.repoURLPoller = p }
}

// NewReconciler creates a new Reconciler with the given configuration.
func NewReconciler(cfg *config.Config, logger *slog.Logger, opts ...ReconcilerOption) *Reconciler {
	r := &Reconciler{
		config:                 cfg,
		logger:                 logger,
		engines:                make(map[string]engine.ExecutionEngine),
		taskRuns:               make(map[string]*taskrun.TaskRun),
		engineChains:           make(map[string][]string),
		streamReaders:          make(map[string]context.CancelFunc),
		prmEvaluators:          make(map[string]*prm.Evaluator),
		ticketCache:            make(map[string]ticketing.Ticket),
		heartbeats:             make(map[string]*watchdog.Heartbeat),
		heartbeatSeqs:          make(map[string]int64),
		podNames:               make(map[string]string),
		taskRunRole:            make(map[string]string),
		taskRunToTournament:    make(map[string]string),
		ticketNotificationRefs: make(map[string]string),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.engineSelector == nil {
		r.engineSelector = NewDefaultEngineSelector(cfg, r.engines)
	}
	if r.intelligentSelector != nil {
		r.intelligentSelector.SetFallback(r.engineSelector)
		r.engineSelector = r.intelligentSelector
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

	// Start the watchdog background loop if one is configured.
	if r.wd != nil {
		go r.wd.Start(ctx, r.runWatchdogChecks)
	}

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
	// Always check running job status — this must run even when at the
	// concurrent job limit, otherwise completed jobs are never reaped and
	// the active count never decreases (causing permanent stall).
	defer r.checkRunningJobs(ctx)

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

	// Drain any follow-up requests from the review poller and submit jobs.
	if r.reviewPoller != nil {
		for _, req := range r.reviewPoller.DrainFollowUps() {
			r.processFollowUpTask(ctx, req)
		}
	}

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

	// Resolve missing repository URL: try to extract from the description,
	// then fall back to async Slack polling. Without a RepoURL the agent
	// cannot push work, so reject rather than burning tokens.
	if ticket.RepoURL == "" {
		if r.resolveRepoURL(ctx, &ticket) {
			// Extraction succeeded — update the cache and continue.
			r.mu.Lock()
			r.ticketCache[ticket.ID] = ticket
			r.mu.Unlock()
		} else if r.repoURLPoller != nil {
			// Start async Slack polling; ProcessTicket returns immediately.
			// Need engine chain for resumption after the URL arrives.
			engineChain := r.engineSelector.SelectEngines(ticket)
			if len(engineChain) == 0 {
				return fmt.Errorf("no registered engines available for ticket %q", ticket.ID)
			}
			return r.startRepoURLPoll(ctx, ticket, idempotencyKey, engineChain)
		} else {
			reason := "no repository URL found in ticket description and no interactive channel configured to ask"
			r.logger.WarnContext(ctx, reason, "ticket_id", ticket.ID)
			if markErr := r.ticketing.MarkFailed(ctx, ticket.ID, reason); markErr != nil {
				r.logger.ErrorContext(ctx, "failed to mark ticket as failed",
					"ticket_id", ticket.ID,
					"error", markErr,
				)
			}
			return fmt.Errorf("%s: %s", ticket.ID, reason)
		}
	}

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

	// Predict cost and auto-reject tasks whose estimate exceeds the budget.
	if r.estimatorPredictor != nil && r.complexityScorer != nil {
		score, scoreErr := r.complexityScorer.Score(ctx, estimator.ComplexityInput{
			TaskDescription: ticket.Description,
			TaskType:        ticket.TicketType,
			RepoURL:         ticket.RepoURL,
			Labels:          ticket.Labels,
		})
		if scoreErr == nil {
			pred, predErr := r.estimatorPredictor.Predict(ctx, *score, engineName)
			if predErr == nil && r.estimatorPredictor.ShouldAutoReject(pred) {
				r.logger.WarnContext(ctx, "auto-rejecting task: predicted cost exceeds threshold",
					"ticket_id", ticket.ID,
					"predicted_cost_high_usd", pred.EstimatedCostHigh,
				)
				return r.ticketing.MarkFailed(ctx, ticket.ID, "predicted cost exceeds configured maximum")
			}
		}
	}

	// When the tournament coordinator is active and enough engines are
	// registered, launch multiple candidate jobs in parallel instead of a
	// single job. launchTournament handles ticket caching, metrics, and
	// notification itself, so we return directly after calling it.
	if r.tournamentCoordinator != nil &&
		r.config.CompetitiveExecution.Enabled &&
		r.config.CompetitiveExecution.DefaultCandidates >= 2 &&
		len(r.engines) >= 2 {
		return r.launchTournament(ctx, ticket)
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
	r.applyContinuationConfig(tr)

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
		tr.ApprovalGateType = "pre_start"

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

		if r.approvalBackend != nil {
			if err := r.approvalBackend.RequestApproval(ctx, tr.HumanQuestion, ticket, tr.ID, []string{"approve", "reject"}); err != nil {
				r.logger.ErrorContext(ctx, "failed to send approval request", "task_run_id", tr.ID, "error", err)
			}
		} else {
			r.logger.WarnContext(ctx, "approval gate active but no approval backend configured; task will wait indefinitely", "task_run_id", tr.ID)
		}

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
			r.logger.InfoContext(ctx, "memory context injected into prompt",
				"ticket_id", ticket.ID,
				"relevant_facts", len(mc.RelevantFacts),
				"engine_insights", len(mc.EngineInsights),
				"known_issues", len(mc.KnownIssues),
			)
		}
	}

	// Build execution spec.
	task := engine.Task{
		ID:            ticket.ID,
		TicketID:      ticket.ID,
		TaskRunID:     tr.ID,
		Title:         ticket.Title,
		Description:   ticket.Description,
		RepoURL:       ticket.RepoURL,
		Labels:        ticket.Labels,
		MemoryContext: memoryContext,
	}

	engineCfg := r.baseEngineConfig(engineName)

	// Send start notification before building the spec so that the returned
	// thread reference can be injected into the container environment, allowing
	// agent pods to post threaded Slack replies via the MCP server.
	threadRef := r.runNotifyStart(ctx, ticket)
	tr.NotificationThreadRef = threadRef
	injectThreadRef(&engineCfg, threadRef)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		return fmt.Errorf("preparing session storage: %w", err)
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

	// Persist state after transition (includes NotificationThreadRef set above).
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
	r.cleanupHintFile(ctx, tr.ID)
	defer r.cleanupPodName(tr.ID)

	// Dispatch review follow-up completions before any other handling.
	if tr.ParentTicketID != "" {
		r.handleFollowUpComplete(ctx, tr)
		return
	}

	// Dispatch to tournament-specific handler when this is a candidate or judge run.
	r.mu.RLock()
	role := r.taskRunRole[tr.ID]
	tournamentID := r.taskRunToTournament[tr.ID]
	r.mu.RUnlock()
	switch role {
	case "candidate":
		r.handleCandidateComplete(ctx, tr, tournamentID)
		return
	case "judge":
		r.handleJudgeComplete(ctx, tr, tournamentID)
		return
	}

	// Check whether to prompt the user for a continuation before treating the
	// job as a terminal success. Turn exhaustion is detected here because the
	// K8s job exits cleanly (JobComplete) even when --max-turns is hit.
	if r.shouldPromptContinuation(tr) {
		r.promptContinuation(ctx, tr)
		return
	}

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
		tr.ApprovalGateType = "pre_merge"
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run to store",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		if r.approvalBackend != nil {
			r.mu.RLock()
			cachedTicketForApproval := r.ticketCache[tr.TicketID]
			r.mu.RUnlock()
			if err := r.approvalBackend.RequestApproval(ctx, tr.HumanQuestion, cachedTicketForApproval, tr.ID, []string{"approve", "reject"}); err != nil {
				r.logger.ErrorContext(ctx, "failed to send pre-merge approval request", "task_run_id", tr.ID, "error", err)
			}
		} else {
			r.logger.WarnContext(ctx, "pre-merge approval gate active but no approval backend configured; task will wait indefinitely", "task_run_id", tr.ID)
		}

		r.logger.InfoContext(ctx, "task run held for pre-merge approval",
			"task_run_id", tr.ID,
			"ticket_id", tr.TicketID,
		)
		return
	}

	// Optional code review gate — only runs when explicitly enabled. When the
	// review backend signals that the diff did not pass, the task run is failed
	// so the ticket is not closed without a passing review.
	if r.config.CodeReview.Enabled && r.reviewBackend != nil {
		timeoutMinutes := r.config.CodeReview.TimeoutMinutes
		if timeoutMinutes <= 0 {
			timeoutMinutes = 15
		}
		reviewCtx, reviewCancel := context.WithTimeout(ctx, time.Duration(timeoutMinutes)*time.Minute)
		defer reviewCancel()
		var diff string
		if tr.Result != nil && tr.Result.BranchName != "" {
			repoURL := r.ticketCacheRepoURL(tr.TicketID)
			scmBackend, scmErr := r.scmFor(repoURL)
			if scmErr == nil {
				// Pass empty baseBranch so the SCM backend resolves the
				// repository's actual default branch rather than assuming "main".
				if d, fetchErr := scmBackend.GetDiff(reviewCtx, repoURL, "", tr.Result.BranchName); fetchErr == nil {
					diff = d
				} else {
					r.logger.WarnContext(ctx, "failed to fetch diff for review gate", "error", fetchErr)
				}
			}
		}
		gateResult, reviewErr := r.reviewBackend.ReviewDiff(reviewCtx, tr.ID, diff)
		if reviewErr != nil {
			r.logger.WarnContext(ctx, "code review failed, continuing without review",
				"task_run_id", tr.ID,
				"error", reviewErr,
			)
		} else {
			r.logger.InfoContext(ctx, "code review completed",
				"task_run_id", tr.ID,
				"passed", gateResult.Passed,
				"summary", gateResult.Summary,
			)
			if !gateResult.Passed {
				r.mu.Lock()
				_ = tr.Transition(taskrun.StateFailed)
				r.mu.Unlock()
				if err := r.taskRunStore.Save(ctx, tr); err != nil {
					r.logger.ErrorContext(ctx, "failed to save task run after review gate failure",
						"task_run_id", tr.ID,
						"error", err,
					)
				}
				metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateFailed)).Inc()
				r.logger.WarnContext(ctx, "task run failed: code review gate did not pass",
					"task_run_id", tr.ID,
					"summary", gateResult.Summary,
				)
				if r.ticketing != nil {
					_ = r.ticketing.MarkFailed(ctx, tr.TicketID, "code review gate did not pass: "+gateResult.Summary)
				}
				return
			}
		}
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
			if err := n.NotifyComplete(ctx, cachedTicket, result, tr.NotificationThreadRef); err != nil {
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

	// Register the opened PR/MR for review comment monitoring.
	if r.reviewPoller != nil && result.MergeRequestURL != "" {
		r.mu.RLock()
		cachedT, hasCachedT := r.ticketCache[tr.TicketID]
		r.mu.RUnlock()
		if hasCachedT {
			r.reviewPoller.Register(
				result.MergeRequestURL,
				tr.TicketID,
				cachedT.Title,
				cachedT.Description,
				cachedT.RepoURL,
			)
		}
	}

	// Record outcome for calibration, routing, and cost estimation.
	r.recordTaskOutcome(ctx, tr, true)
	r.cleanupHeartbeat(tr.ID)

	r.logger.InfoContext(ctx, "task run succeeded",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"duration", time.Since(tr.CreatedAt),
	)
}

// handleFollowUpComplete processes the completion of a review follow-up job.
// It posts a comment on the original ticket and, if configured, replies to
// the originating review comment and resolves its thread.
func (r *Reconciler) handleFollowUpComplete(ctx context.Context, tr *taskrun.TaskRun) {
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateSucceeded); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition follow-up task run to succeeded",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save follow-up task run",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()

	r.mu.RLock()
	result := engine.TaskResult{Success: true, Summary: "review follow-up completed successfully"}
	if tr.Result != nil {
		result = *tr.Result
	}
	r.mu.RUnlock()
	tr.Result = &result

	// Post a comment on the original ticket.
	if r.ticketing != nil {
		msg := "✅ Review follow-up complete.\n\n**Summary:** " + result.Summary
		if err := r.ticketing.AddComment(ctx, tr.ParentTicketID, msg); err != nil {
			r.logger.WarnContext(ctx, "failed to add follow-up completion comment",
				"ticket_id", tr.ParentTicketID,
				"error", err,
			)
		}
	}

	// Reply to the original review comment if configured.
	if tr.ReviewCommentID != "" && tr.ReviewPRURL != "" {
		backend, scmErr := r.scmFor(tr.ReviewPRURL)
		if scmErr == nil {
			replyBody := "Addressed. " + result.Summary
			if replyErr := backend.ReplyToComment(ctx, tr.ReviewPRURL, tr.ReviewCommentID, replyBody); replyErr != nil {
				r.logger.WarnContext(ctx, "failed to reply to review comment",
					"pr_url", tr.ReviewPRURL,
					"comment_id", tr.ReviewCommentID,
					"error", replyErr,
				)
			}
		}

		// Resolve the discussion thread if configured and a thread ID was set.
		if r.config.ReviewResponse.ResolveThreads && tr.ReviewThreadID != "" {
			if backend == nil {
				backend, scmErr = r.scmFor(tr.ReviewPRURL)
			}
			if scmErr == nil {
				if resolveErr := backend.ResolveThread(ctx, tr.ReviewPRURL, tr.ReviewThreadID); resolveErr != nil {
					r.logger.WarnContext(ctx, "failed to resolve review thread",
						"pr_url", tr.ReviewPRURL,
						"thread_id", tr.ReviewThreadID,
						"error", resolveErr,
					)
				}
			}
		}
	}

	r.recordTaskOutcome(ctx, tr, true)
	r.cleanupHeartbeat(tr.ID)

	r.logger.InfoContext(ctx, "review follow-up task run succeeded",
		"task_run_id", tr.ID,
		"parent_ticket_id", tr.ParentTicketID,
		"duration", time.Since(tr.CreatedAt),
	)
}

// processFollowUpTask creates and launches a K8s Job for a review follow-up
// request. Unlike ProcessTicket, it skips ticketing backend calls (no
// MarkInProgress) and sets the review-specific TaskRun fields.
func (r *Reconciler) processFollowUpTask(ctx context.Context, req reviewpoller.FollowUpRequest) {
	// Derive a synthetic ticket for engine selection and job building.
	ticket := ticketing.Ticket{
		ID:          fmt.Sprintf("%s-review-%d", req.TicketID, time.Now().UnixMilli()),
		Title:       req.OriginalTitle + " [review follow-up]",
		Description: req.EnrichedDescription,
		RepoURL:     req.RepoURL,
		TicketType:  "issue",
	}

	engineChain := r.engineSelector.SelectEngines(ticket)
	if len(engineChain) == 0 {
		r.logger.WarnContext(ctx, "no engine available for review follow-up",
			"ticket_id", req.TicketID,
		)
		return
	}
	engineName := engineChain[0]
	eng, ok := r.engines[engineName]
	if !ok {
		r.logger.WarnContext(ctx, "engine not registered for review follow-up",
			"engine", engineName,
		)
		return
	}

	idempotencyKey := fmt.Sprintf("%s-1", ticket.ID)
	tr := taskrun.New(
		fmt.Sprintf("tr-%s-%d", ticket.ID, time.Now().UnixMilli()),
		idempotencyKey,
		ticket.ID,
		engineName,
	)
	tr.CurrentEngine = engineName
	tr.EngineAttempts = []string{engineName}
	tr.ParentTicketID = req.TicketID
	tr.ReviewCommentID = req.ReplyCommentID
	tr.ReviewThreadID = req.ThreadID
	tr.ReviewPRURL = req.PRURL

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save review follow-up task run",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	task := engine.Task{
		ID:          ticket.ID,
		TicketID:    ticket.ID,
		TaskRunID:   tr.ID,
		Title:       ticket.Title,
		Description: ticket.Description,
		RepoURL:     ticket.RepoURL,
	}

	engineCfg := r.baseEngineConfig(engineName)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to prepare session storage for review follow-up",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build execution spec for review follow-up",
			"ticket_id", req.TicketID,
			"error", err,
		)
		return
	}

	if r.jobBuilder == nil {
		r.logger.WarnContext(ctx, "no job builder configured, skipping review follow-up")
		return
	}

	job, err := r.jobBuilder.Build(tr.ID, engineName, spec)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build K8s job for review follow-up",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	if r.k8sClient != nil {
		if _, err := r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			r.logger.ErrorContext(ctx, "failed to create K8s job for review follow-up",
				"task_run_id", tr.ID,
				"error", err,
			)
			return
		}
	}

	if err := tr.Transition(taskrun.StateRunning); err != nil {
		r.logger.ErrorContext(ctx, "failed to transition review follow-up to running",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	tr.JobName = job.Name

	r.mu.Lock()
	r.taskRuns[idempotencyKey] = tr
	r.engineChains[idempotencyKey] = engineChain
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save review follow-up task run after launch",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Inc()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()

	if engineName == "claude-code" {
		r.startStreamReader(ctx, tr)
	}

	r.logger.InfoContext(ctx, "review follow-up job created",
		"parent_ticket_id", req.TicketID,
		"pr_url", req.PRURL,
		"task_run_id", tr.ID,
		"engine", engineName,
		"job", job.Name,
	)
}

// scmFor returns the appropriate SCM backend for the given URL.
func (r *Reconciler) scmFor(url string) (scm.Backend, error) {
	if r.scmRouter != nil {
		return r.scmRouter.For(url)
	}
	if r.scmBackend != nil {
		return r.scmBackend, nil
	}
	return nil, fmt.Errorf("no SCM backend configured")
}

// ticketCacheRepoURL returns the cached RepoURL for the given ticket ID.
func (r *Reconciler) ticketCacheRepoURL(ticketID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.ticketCache[ticketID]; ok {
		return t.RepoURL
	}
	return ""
}

// handleJobFailed processes a failed job, attempting engine fallback or
// retrying with the same engine if allowed.
func (r *Reconciler) handleJobFailed(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.cancelStreamReader(tr.ID)
	r.cleanupPRMEvaluator(tr.ID)
	r.cleanupHintFile(ctx, tr.ID)
	defer r.cleanupPodName(tr.ID)

	// For tournament candidates/judges, record failure with the coordinator
	// before falling through to normal failure handling.
	r.mu.RLock()
	role := r.taskRunRole[tr.ID]
	tournamentID := r.taskRunToTournament[tr.ID]
	r.mu.RUnlock()
	if role == "candidate" && tournamentID != "" {
		result := &tournament.CandidateResult{
			TaskRunID: tr.ID,
			Engine:    tr.CurrentEngine,
			Success:   false,
			Summary:   reason,
		}
		if _, err := r.tournamentCoordinator.OnCandidateComplete(ctx, tournamentID, result); err != nil {
			r.logger.WarnContext(ctx, "recording failed candidate in tournament",
				"task_run_id", tr.ID,
				"tournament_id", tournamentID,
				"error", err,
			)
		}
	}
	if role == "judge" && tournamentID != "" {
		if err := r.tournamentCoordinator.CancelTournament(ctx, tournamentID); err != nil {
			r.logger.WarnContext(ctx, "cancelling tournament after judge failure",
				"tournament_id", tournamentID,
				"error", err,
			)
		}
	}

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
		// Optionally enrich the retry with diagnosis-based instructions.
		retryPrompt := ""
		shouldDoRetry := true
		if r.analyser != nil && r.config.Diagnosis.Enabled {
			diag, diagErr := r.analyser.Analyse(ctx, diagnosis.DiagnosisInput{
				TaskRun:        tr,
				WatchdogReason: reason,
				Result:         tr.Result,
			})
			if diagErr == nil && diag != nil {
				maxDiag := r.config.Diagnosis.MaxDiagnosesPerTask
				if maxDiag <= 0 {
					maxDiag = 3
				}
				shouldDoRetry = diagnosis.ShouldRetry(tr.DiagnosisHistory, diag, maxDiag)
				tr.DiagnosisHistory = append(tr.DiagnosisHistory, diagnosis.ToDiagnosisRecord(diag))
				if shouldDoRetry && r.retryBuilder != nil {
					if cachedTicket, ok := r.ticketCache[tr.TicketID]; ok {
						spec, specErr := r.retryBuilder.Build(ctx, cachedTicket.Description, diag, tr.CurrentEngine, r.config.Diagnosis.EnableEngineSwitch)
						if specErr == nil && spec != nil {
							retryPrompt = spec.Prompt
							if spec.Engine != tr.CurrentEngine {
								tr.CurrentEngine = spec.Engine
								tr.EngineAttempts = append(tr.EngineAttempts, spec.Engine)
							}
						}
					}
				}
			}
		}

		if shouldDoRetry {
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
			r.launchRetryJob(ctx, tr, retryPrompt)
			return
		}
		// Diagnosis advises against retry; fall through to terminal failure.
		r.logger.InfoContext(ctx, "diagnosis skipping retry, terminating task run",
			"task_run_id", tr.ID,
		)
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

	// Record outcome for calibration, routing, and cost estimation.
	r.recordTaskOutcome(ctx, tr, false)
	r.cleanupHeartbeat(tr.ID)

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
		ID:        tr.TicketID,
		TicketID:  tr.TicketID,
		TaskRunID: tr.ID,
	}

	engineCfg := r.baseEngineConfig(engineName)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to prepare session storage for fallback job",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
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

// ResolveApproval processes an approval or rejection callback for the given
// task run. It transitions the TaskRun based on the gate type and approval
// decision, launching a job on pre-start approval or completing the task on
// pre-merge approval.
func (r *Reconciler) ResolveApproval(ctx context.Context, taskRunID string, approved bool, responder string) error {
	r.mu.Lock()
	var target *taskrun.TaskRun
	for _, tr := range r.taskRuns {
		if tr.ID == taskRunID {
			target = tr
			break
		}
	}
	r.mu.Unlock()

	if target == nil {
		return fmt.Errorf("task run %q not found", taskRunID)
	}

	if target.State != taskrun.StateNeedsHuman {
		return fmt.Errorf("task run %q is in state %q, not NeedsHuman", taskRunID, target.State)
	}

	if !approved {
		r.mu.Lock()
		if err := target.Transition(taskrun.StateFailed); err != nil {
			r.mu.Unlock()
			return fmt.Errorf("transitioning task run to failed: %w", err)
		}
		r.mu.Unlock()

		if err := r.taskRunStore.Save(ctx, target); err != nil {
			r.logger.ErrorContext(ctx, "failed to save task run after rejection",
				"task_run_id", taskRunID,
				"error", err,
			)
		}

		metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateFailed)).Inc()

		failureReason := "rejected by " + responder
		if target.ApprovalGateType == "continuation" {
			failureReason = "continuation stopped by " + responder
			if target.Result != nil && target.Result.Summary != "" {
				failureReason += "; progress: " + target.Result.Summary
			}
		}
		if r.ticketing != nil {
			_ = r.ticketing.MarkFailed(ctx, target.TicketID, failureReason)
		}

		r.logger.InfoContext(ctx, "task run rejected",
			"task_run_id", taskRunID,
			"responder", responder,
			"gate_type", target.ApprovalGateType,
		)
		return nil
	}

	switch target.ApprovalGateType {
	case "pre_start":
		return r.resolvePreStartApproval(ctx, target)
	case "pre_merge":
		return r.resolvePreMergeApproval(ctx, target)
	case "continuation":
		return r.resolveContinuationApproval(ctx, target)
	case "missing_repo_url":
		// The repo URL has already been written to the ticket cache by
		// pollRepoURL before calling ResolveApproval. Reuse the
		// pre-start resolver to launch the job.
		return r.resolvePreStartApproval(ctx, target)
	default:
		return fmt.Errorf("unknown approval gate type %q for task run %q", target.ApprovalGateType, taskRunID)
	}
}

// resolvePreStartApproval launches the job for a task run that was held at the
// pre-start approval gate.
func (r *Reconciler) resolvePreStartApproval(ctx context.Context, tr *taskrun.TaskRun) error {
	r.mu.RLock()
	engineChain := r.engineChains[tr.IdempotencyKey]
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	if !hasTicket {
		return fmt.Errorf("ticket %q not found in cache", tr.TicketID)
	}

	engineName := tr.Engine
	eng, ok := r.engines[engineName]
	if !ok {
		return fmt.Errorf("engine %q not registered", engineName)
	}

	// Transition NeedsHuman → Running.
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateRunning); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("transitioning task run to running: %w", err)
	}
	r.mu.Unlock()

	// Query episodic memory for prior knowledge relevant to this task.
	var memoryContext string
	if r.memoryQuery != nil {
		mc, qErr := r.memoryQuery.QueryForTask(ctx, cachedTicket.Description, cachedTicket.RepoURL, engineName, "")
		if qErr != nil {
			r.logger.WarnContext(ctx, "memory query failed, continuing without prior knowledge",
				"ticket_id", cachedTicket.ID,
				"error", qErr,
			)
		} else if mc != nil && mc.FormattedSection != "" {
			memoryContext = mc.FormattedSection
		}
	}

	task := engine.Task{
		ID:            cachedTicket.ID,
		TicketID:      cachedTicket.ID,
		TaskRunID:     tr.ID,
		Title:         cachedTicket.Title,
		Description:   cachedTicket.Description,
		RepoURL:       cachedTicket.RepoURL,
		Labels:        cachedTicket.Labels,
		MemoryContext: memoryContext,
	}

	engineCfg := r.baseEngineConfig(engineName)

	// Send start notification before building the spec so that the returned
	// thread reference can be injected into the container environment.
	threadRef := r.runNotifyStart(ctx, cachedTicket)
	tr.NotificationThreadRef = threadRef
	injectThreadRef(&engineCfg, threadRef)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		return fmt.Errorf("preparing session storage: %w", err)
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		return fmt.Errorf("building execution spec: %w", err)
	}

	if r.jobBuilder == nil {
		return fmt.Errorf("no job builder configured")
	}

	job, err := r.jobBuilder.Build(tr.ID, engineName, spec)
	if err != nil {
		return fmt.Errorf("building k8s job: %w", err)
	}

	if r.k8sClient != nil {
		_, err = r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating k8s job: %w", err)
		}
	}

	tr.JobName = job.Name

	// Persist state (includes NotificationThreadRef set above).
	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run after pre-start approval",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Inc()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()

	if err := r.ticketing.MarkInProgress(ctx, cachedTicket.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to mark ticket in progress after approval",
			"ticket_id", cachedTicket.ID,
			"error", err,
		)
	}

	if engineName == "claude-code" {
		r.startStreamReader(ctx, tr)
	}

	_ = engineChain // used during initial selection, preserved for fallback

	r.logger.InfoContext(ctx, "pre-start approval granted, job launched",
		"task_run_id", tr.ID,
		"engine", engineName,
		"job", job.Name,
	)
	return nil
}

// resolvePreMergeApproval completes a task run that was held at the pre-merge
// approval gate.
func (r *Reconciler) resolvePreMergeApproval(ctx context.Context, tr *taskrun.TaskRun) error {
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateSucceeded); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("transitioning task run to succeeded: %w", err)
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run after pre-merge approval",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()
	metrics.TaskRunDurationSeconds.WithLabelValues(tr.Engine).Observe(
		time.Since(tr.CreatedAt).Seconds(),
	)

	r.mu.RLock()
	result := engine.TaskResult{Success: true, Summary: "task completed successfully"}
	if tr.Result != nil {
		result = *tr.Result
	}
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()
	tr.Result = &result

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
			if err := n.NotifyComplete(ctx, cachedTicket, result, tr.NotificationThreadRef); err != nil {
				r.logger.ErrorContext(ctx, "completion notification failed",
					"ticket_id", tr.TicketID,
					"error", err,
				)
			}
		}
	}

	if r.memoryExtractor != nil {
		go r.extractMemory(ctx, tr)
	}

	if r.reviewPoller != nil && result.MergeRequestURL != "" {
		r.mu.RLock()
		cachedT, hasCachedT := r.ticketCache[tr.TicketID]
		r.mu.RUnlock()
		if hasCachedT {
			r.reviewPoller.Register(
				result.MergeRequestURL,
				tr.TicketID,
				cachedT.Title,
				cachedT.Description,
				cachedT.RepoURL,
			)
		}
	}

	r.recordTaskOutcome(ctx, tr, true)
	r.cleanupHeartbeat(tr.ID)

	r.logger.InfoContext(ctx, "pre-merge approval granted, task run succeeded",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
	)
	return nil
}

// applyContinuationConfig sets the continuation-related fields on a freshly
// created TaskRun from the controller's current engine config.
func (r *Reconciler) applyContinuationConfig(tr *taskrun.TaskRun) {
	if r.config.Engines.ClaudeCode == nil {
		return
	}
	cc := r.config.Engines.ClaudeCode
	maxTurns := cc.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50 // mirrors defaultMaxTurns in the engine package
	}
	tr.ConfiguredMaxTurns = maxTurns

	if cc.ContinuationPrompt {
		maxConts := cc.MaxContinuations
		if maxConts <= 0 {
			maxConts = 3
		}
		tr.MaxContinuations = maxConts
	}
}

// shouldPromptContinuation returns true when all preconditions for a
// user-prompted continuation are met:
//   - continuation_prompt is enabled in config
//   - a session store is configured (continuation requires --resume)
//   - an approval backend is available to send the prompt
//   - the agent did not declare success
//   - tool-call count meets or exceeds the configured max-turns limit
//   - the TaskRun has not yet reached its continuation limit
func (r *Reconciler) shouldPromptContinuation(tr *taskrun.TaskRun) bool {
	if r.config.Engines.ClaudeCode == nil || !r.config.Engines.ClaudeCode.ContinuationPrompt {
		return false
	}
	if r.approvalBackend == nil {
		return false
	}
	if tr.MaxContinuations <= 0 || tr.ContinuationCount >= tr.MaxContinuations {
		return false
	}
	if tr.ConfiguredMaxTurns <= 0 || tr.ToolCallsTotal < tr.ConfiguredMaxTurns {
		return false
	}
	if tr.Result != nil && tr.Result.Success {
		return false
	}
	return true
}

// promptContinuation transitions the TaskRun to NeedsHuman and sends an
// approval request asking the user whether to continue or stop.
func (r *Reconciler) promptContinuation(ctx context.Context, tr *taskrun.TaskRun) {
	question := r.buildContinuationQuestion(tr)

	r.mu.Lock()
	if err := tr.Transition(taskrun.StateNeedsHuman); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition task run to needs human for continuation",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	tr.HumanQuestion = question
	tr.ApprovalGateType = "continuation"
	r.mu.Unlock()

	// The original K8s job has completed; decrement before waiting for approval
	// to mirror the pre_merge gate flow (resolvePreMergeApproval decrements before
	// launching the review job).
	metrics.ActiveJobs.Dec()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run before continuation prompt",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	r.mu.RLock()
	cachedTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	if cachedTicket.ID == "" {
		r.logger.WarnContext(ctx, "ticket not in cache for continuation prompt",
			"task_run_id", tr.ID,
			"ticket_id", tr.TicketID,
		)
	}

	if err := r.approvalBackend.RequestApproval(ctx, question, cachedTicket, tr.ID, []string{"continue", "stop"}); err != nil {
		r.logger.ErrorContext(ctx, "failed to send continuation approval request",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	r.logger.InfoContext(ctx, "task run held for continuation approval",
		"task_run_id", tr.ID,
		"ticket_id", tr.TicketID,
		"tool_calls", tr.ToolCallsTotal,
		"continuation_count", tr.ContinuationCount,
		"max_continuations", tr.MaxContinuations,
	)
}

// buildContinuationQuestion formats the human-readable continuation prompt
// shown to the approver, including progress context from the current result.
func (r *Reconciler) buildContinuationQuestion(tr *taskrun.TaskRun) string {
	summary := "no summary available"
	costStr := ""
	if tr.Result != nil {
		if tr.Result.Summary != "" {
			summary = tr.Result.Summary
		}
		if tr.Result.CostEstimateUSD > 0 {
			costStr = fmt.Sprintf("  Cost so far: $%.2f\n", tr.Result.CostEstimateUSD)
		}
	}
	return fmt.Sprintf(
		"The agent ran out of turns (%d/%d used) on this task.\n\nProgress so far:\n  Turns used: %d\n%s  Summary: %q\n\nThis is continuation %d of %d allowed. Continue with another %d turns?",
		tr.ToolCallsTotal, tr.ConfiguredMaxTurns,
		tr.ToolCallsTotal,
		costStr,
		summary,
		tr.ContinuationCount+1, tr.MaxContinuations,
		tr.ConfiguredMaxTurns,
	)
}

// resolveContinuationApproval launches a new job that resumes the agent
// session where it left off. RetryCount is not incremented — continuations
// are tracked separately via ContinuationCount.
func (r *Reconciler) resolveContinuationApproval(ctx context.Context, tr *taskrun.TaskRun) error {
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateRunning); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("transitioning task run to running for continuation: %w", err)
	}
	tr.ContinuationCount++
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run after continuation approval",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	r.logger.InfoContext(ctx, "continuation approved, launching resume job",
		"task_run_id", tr.ID,
		"continuation_count", tr.ContinuationCount,
	)

	r.launchContinuationJob(ctx, tr)
	return nil
}

// launchContinuationJob creates a new K8s Job that resumes the existing
// Claude Code session via --resume. Unlike launchRetryJob, it does not
// increment RetryCount or inject diagnosis prompts.
func (r *Reconciler) launchContinuationJob(ctx context.Context, tr *taskrun.TaskRun) {
	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	eng, ok := r.engines[tr.CurrentEngine]
	if !ok {
		r.logger.ErrorContext(ctx, "continuation engine not registered",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
		)
		return
	}

	desc := ""
	if hasTicket {
		desc = cachedTicket.Description
	}

	task := engine.Task{
		ID:        tr.TicketID,
		TicketID:  tr.TicketID,
		TaskRunID: tr.ID,
		SessionID: tr.SessionID,
	}
	if hasTicket {
		task.Title = cachedTicket.Title
		task.RepoURL = cachedTicket.RepoURL
		task.Labels = cachedTicket.Labels
	}
	task.Description = desc

	engineCfg := r.baseEngineConfig(tr.CurrentEngine)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to prepare session storage for continuation job",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build continuation execution spec",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	if r.jobBuilder == nil {
		return
	}

	jobID := fmt.Sprintf("%s-c%d", tr.ID, tr.ContinuationCount)
	job, err := r.jobBuilder.Build(jobID, tr.CurrentEngine, spec)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build continuation k8s job",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	if r.k8sClient != nil {
		_, err = r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			r.logger.ErrorContext(ctx, "failed to create continuation k8s job",
				"engine", tr.CurrentEngine,
				"task_run_id", tr.ID,
				"error", err,
			)
			return
		}
	}

	r.mu.Lock()
	tr.JobName = job.Name
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run after continuation job launch",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Inc()

	if tr.CurrentEngine == "claude-code" {
		r.startStreamReader(ctx, tr)
	}

	r.logger.InfoContext(ctx, "continuation job created",
		"engine", tr.CurrentEngine,
		"job", job.Name,
		"task_run_id", tr.ID,
		"continuation_count", tr.ContinuationCount,
	)
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

		// Feed stream events into the watchdog for real-time telemetry tracking.
		if r.wd != nil {
			wdProcessor := func(_ context.Context, event *agentstream.StreamEvent) {
				r.mu.Lock()
				r.wd.ConsumeStreamEvent(tr, event)
				r.mu.Unlock()
			}
			forwarderOpts = append(forwarderOpts, agentstream.WithEventProcessor(wdProcessor))
		}

		// Forward events to the transcript sink for audit archival.
		if r.transcriptSink != nil {
			transcriptProcessor := func(sinkCtx context.Context, event *agentstream.StreamEvent) {
				if err := r.transcriptSink.Append(sinkCtx, tr.ID, event); err != nil {
					r.logger.Warn("transcript append failed",
						"task_run_id", tr.ID,
						"error", err,
					)
				}
			}
			forwarderOpts = append(forwarderOpts, agentstream.WithEventProcessor(transcriptProcessor))
		}

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
			r.mu.Lock()
			r.podNames[tr.ID] = podName
			r.mu.Unlock()

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

		// Flush the transcript now that all events for this task run have been
		// forwarded (successful completion or context cancellation on failure).
		if r.transcriptSink != nil {
			if err := r.transcriptSink.Flush(context.Background(), tr.ID); err != nil {
				r.logger.Warn("transcript flush failed",
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

// recordPRMHint logs the PRM intervention, increments metrics, and delivers
// the hint content to the running agent pod via writeHintFile. The file write
// is performed asynchronously so the stream reader is not blocked.
func (r *Reconciler) recordPRMHint(ctx context.Context, tr *taskrun.TaskRun, intervention *prm.Intervention) {
	r.logger.InfoContext(ctx, "prm nudge recorded",
		"task_run_id", tr.ID,
		"reason", intervention.Reason,
		"hint_length", len(intervention.HintContent),
	)

	metrics.PRMInterventionsTotal.WithLabelValues(string(intervention.Action)).Inc()

	if intervention.HintContent == "" || r.restConfig == nil {
		return
	}

	// Capture values for the goroutine before r.mu can be modified.
	taskRunID := tr.ID
	content := intervention.HintContent

	go func() {
		writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.writeHintFile(writeCtx, taskRunID, content); err != nil {
			r.logger.Warn("prm hint file write failed",
				"task_run_id", taskRunID,
				"error", err,
			)
		}
	}()
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

// runNotifyStart fires NotifyStart on every configured notifier and returns
// the first non-empty thread reference received. The ref is also stored in
// ticketNotificationRefs so that tournament completions (which have no task
// run reference) can look it up by ticket ID.
func (r *Reconciler) runNotifyStart(ctx context.Context, ticket ticketing.Ticket) string {
	var threadRef string
	for _, n := range r.notifiers {
		ref, err := n.NotifyStart(ctx, ticket)
		if err != nil {
			r.logger.ErrorContext(ctx, "notification failed",
				"channel", n.Name(),
				"error", err,
			)
			continue
		}
		if threadRef == "" && ref != "" {
			threadRef = ref
		}
	}
	if threadRef != "" {
		r.mu.Lock()
		r.ticketNotificationRefs[ticket.ID] = threadRef
		r.mu.Unlock()
	}
	return threadRef
}

// injectThreadRef adds SLACK_THREAD_TS to the engine config's Env map so that
// agent pods can post threaded Slack messages via the MCP server.
func injectThreadRef(cfg *engine.EngineConfig, threadRef string) {
	if threadRef == "" {
		return
	}
	if cfg.Env == nil {
		cfg.Env = make(map[string]string)
	}
	cfg.Env["SLACK_THREAD_TS"] = threadRef
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

// launchRetryJob builds and creates a new K8s Job for a same-engine retry.
// If prompt is non-empty it overrides the original ticket description; this
// allows the diagnosis subsystem to inject corrective instructions.
func (r *Reconciler) launchRetryJob(ctx context.Context, tr *taskrun.TaskRun, prompt string) {
	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	eng, ok := r.engines[tr.CurrentEngine]
	if !ok {
		r.logger.ErrorContext(ctx, "retry engine not registered",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
		)
		return
	}

	desc := ""
	if hasTicket {
		desc = cachedTicket.Description
	}
	if prompt != "" {
		desc = prompt
	}

	task := engine.Task{
		ID:        tr.TicketID,
		TicketID:  tr.TicketID,
		TaskRunID: tr.ID,
	}
	if hasTicket {
		task.Title = cachedTicket.Title
		task.RepoURL = cachedTicket.RepoURL
		task.Labels = cachedTicket.Labels
	}
	task.Description = desc

	// If the previous attempt pushed to a branch, pass it so the retry
	// agent can clone that branch and continue from the prior work.
	if tr.Result != nil && tr.Result.BranchName != "" {
		task.PriorBranchName = tr.Result.BranchName
	} else if tr.RetryCount > 0 {
		// Fall back to the predictable naming convention even if result.json
		// was not written (e.g. pod killed before stop hook ran).
		task.PriorBranchName = "osmia/" + tr.TicketID
	}

	engineCfg := r.baseEngineConfig(tr.CurrentEngine)

	if err := r.prepareSession(ctx, tr.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to prepare session storage for retry job",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	spec, err := eng.BuildExecutionSpec(task, engineCfg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build retry execution spec",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	if r.jobBuilder == nil {
		return
	}

	jobID := fmt.Sprintf("%s-r%d", tr.ID, tr.RetryCount)
	job, err := r.jobBuilder.Build(jobID, tr.CurrentEngine, spec)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build retry k8s job",
			"engine", tr.CurrentEngine,
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}

	if r.k8sClient != nil {
		_, err = r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			r.logger.ErrorContext(ctx, "failed to create retry k8s job",
				"engine", tr.CurrentEngine,
				"task_run_id", tr.ID,
				"error", err,
			)
			return
		}
	}

	r.mu.Lock()
	if err := tr.Transition(taskrun.StateRunning); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition task run to running after retry",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	tr.JobName = job.Name
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run to store after retry launch",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Inc()

	if tr.CurrentEngine == "claude-code" {
		r.startStreamReader(ctx, tr)
	}

	r.logger.InfoContext(ctx, "retry job created",
		"engine", tr.CurrentEngine,
		"job", job.Name,
		"task_run_id", tr.ID,
		"retry_count", tr.RetryCount,
	)
}

// recordTaskOutcome feeds the result of a completed or failed TaskRun into
// the watchdog calibrator, intelligent routing fingerprints, and cost estimator.
func (r *Reconciler) recordTaskOutcome(ctx context.Context, tr *taskrun.TaskRun, success bool) {
	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()

	costUSD := 0.0
	if tr.Result != nil {
		costUSD = tr.Result.CostEstimateUSD
	}
	duration := time.Since(tr.CreatedAt)

	// Watchdog calibration.
	if r.calibrator != nil {
		repoURL, taskType := "", ""
		if hasTicket {
			repoURL = cachedTicket.RepoURL
			taskType = cachedTicket.TicketType
		}
		obs := watchdog.Observation{
			RepoURL:              repoURL,
			Engine:               tr.CurrentEngine,
			TaskType:             taskType,
			TokensConsumed:       int64(tr.TokensConsumed),
			ToolCallsTotal:       tr.ToolCallsTotal,
			FilesChanged:         tr.FilesChanged,
			CostEstimateUSD:      costUSD,
			DurationSeconds:      duration.Seconds(),
			ConsecutiveIdentical: tr.ConsecutiveIdenticalTools,
			CompletedAt:          time.Now(),
		}
		r.calibrator.Record(ctx, obs)

		if r.profileResolver != nil {
			key := watchdog.ProfileKey{
				RepoPattern: repoURL,
				Engine:      tr.CurrentEngine,
				TaskType:    taskType,
			}
			r.profileResolver.RefreshProfile(ctx, key)
		}
	}

	if !hasTicket {
		return
	}

	// Intelligent routing outcome.
	if r.intelligentSelector != nil {
		_ = r.intelligentSelector.RecordOutcome(ctx, routing.TaskOutcome{
			EngineName:   tr.CurrentEngine,
			TaskType:     cachedTicket.TicketType,
			RepoLanguage: labelValue(cachedTicket.Labels, "lang:"),
			Complexity:   labelValue(cachedTicket.Labels, "complexity:"),
			Success:      success,
			Duration:     duration,
			Cost:         costUSD,
		})
	}

	// Cost/duration estimator outcome.
	if r.estimatorPredictor != nil && r.complexityScorer != nil {
		score, scoreErr := r.complexityScorer.Score(ctx, estimator.ComplexityInput{
			TaskDescription: cachedTicket.Description,
			TaskType:        cachedTicket.TicketType,
			RepoURL:         cachedTicket.RepoURL,
			Labels:          cachedTicket.Labels,
		})
		if scoreErr == nil {
			_ = r.estimatorPredictor.RecordOutcome(ctx, estimator.PredictionOutcome{
				ComplexityScore: *score,
				Engine:          tr.CurrentEngine,
				ActualCost:      costUSD,
				ActualDuration:  duration,
				Success:         success,
				TaskRunID:       tr.ID,
				RecordedAt:      time.Now(),
			})
		}
	}
}

// buildHeartbeat constructs a watchdog Heartbeat from the current TaskRun state.
func (r *Reconciler) buildHeartbeat(tr *taskrun.TaskRun, seq int64) *watchdog.Heartbeat {
	hb := &watchdog.Heartbeat{
		Seq:                       seq,
		RunID:                     tr.ID,
		Timestamp:                 time.Now(),
		TokensConsumed:            int64(tr.TokensConsumed),
		FilesChanged:              tr.FilesChanged,
		ToolCallsTotal:            tr.ToolCallsTotal,
		LastToolName:              tr.LastToolName,
		ConsecutiveIdenticalCalls: tr.ConsecutiveIdenticalTools,
		CostEstimateUSD:           tr.CostUSD,
	}
	// Fall back to the result cost when live stream cost is not yet available.
	if hb.CostEstimateUSD == 0 && tr.Result != nil {
		hb.CostEstimateUSD = tr.Result.CostEstimateUSD
	}
	return hb
}

// runWatchdogChecks is the callback invoked by the watchdog loop on each
// tick. It iterates all running TaskRuns and checks each one.
func (r *Reconciler) runWatchdogChecks(ctx context.Context) {
	r.mu.RLock()
	running := make([]*taskrun.TaskRun, 0, len(r.taskRuns))
	for _, tr := range r.taskRuns {
		if tr.State == taskrun.StateRunning || tr.State == taskrun.StateNeedsHuman {
			running = append(running, tr)
		}
	}
	r.mu.RUnlock()

	for _, tr := range running {
		r.runWatchdogCheck(ctx, tr)
	}
}

// runWatchdogCheck evaluates the watchdog rules for a single TaskRun,
// terminating it if an anomaly is confirmed.
func (r *Reconciler) runWatchdogCheck(ctx context.Context, tr *taskrun.TaskRun) {
	r.mu.Lock()
	seq := r.heartbeatSeqs[tr.ID] + 1
	r.heartbeatSeqs[tr.ID] = seq
	current := r.buildHeartbeat(tr, seq)
	previous := r.heartbeats[tr.ID]
	r.heartbeats[tr.ID] = current
	r.mu.Unlock()

	wdReason, action, err := r.wd.Check(tr, current, previous)
	if err != nil || wdReason == nil {
		return
	}

	r.logger.WarnContext(ctx, "watchdog anomaly: terminating task run",
		"task_run_id", tr.ID,
		"reason_code", wdReason.ReasonCode,
		"action", string(action),
	)

	switch action {
	case watchdog.ActionTerminate, watchdog.ActionTerminateWithFeedback, watchdog.ActionTerminateAndNotify:
		r.handleJobFailed(ctx, tr, wdReason.Message)
		if r.approvalBackend != nil {
			_ = r.approvalBackend.CancelPending(ctx, tr.ID)
		}
	default:
		// ActionWarn — already logged above.
	}
}

// cleanupHeartbeat removes all watchdog heartbeat state for a completed or
// failed TaskRun. Safe to call even when no heartbeat data exists.
func (r *Reconciler) cleanupHeartbeat(taskRunID string) {
	r.mu.Lock()
	delete(r.heartbeats, taskRunID)
	delete(r.heartbeatSeqs, taskRunID)
	r.mu.Unlock()
}

// labelValue extracts a label value with the given prefix from a label slice.
// For example, labelValue(labels, "lang:") returns "go" for the label "lang:go".
func labelValue(labels []string, prefix string) string {
	for _, l := range labels {
		if len(l) > len(prefix) && l[:len(prefix)] == prefix {
			return l[len(prefix):]
		}
	}
	return ""
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

// validateHintPath verifies that a configured PRM hint file path does not
// contain path traversal components. Returns an error if any component is "..".
func validateHintPath(path string) error {
	if path == "" {
		return fmt.Errorf("hint file path is empty")
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return fmt.Errorf("hint file path %q contains path traversal", path)
		}
	}
	return nil
}

// writeHintFile delivers PRM hint content to the running agent pod by
// executing a tee command inside the container via the Kubernetes exec API.
// The hint path defaults to /workspace/.osmia-hint.md when not configured.
func (r *Reconciler) writeHintFile(ctx context.Context, taskRunID, content string) error {
	if r.restConfig == nil || r.k8sClient == nil {
		return fmt.Errorf("rest config or k8s client not available for hint write")
	}

	hintPath := r.config.PRM.HintFilePath
	if hintPath == "" {
		hintPath = "/workspace/.osmia-hint.md"
	}
	if err := validateHintPath(hintPath); err != nil {
		return fmt.Errorf("invalid hint file path: %w", err)
	}

	r.mu.RLock()
	podName := r.podNames[taskRunID]
	r.mu.RUnlock()

	if podName == "" {
		return fmt.Errorf("pod name not yet resolved for task run %q", taskRunID)
	}

	req := r.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(r.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "agent",
			Command:   []string{"tee", hintPath},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, k8sscheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(r.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("creating SPDY executor for hint write: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  strings.NewReader(content),
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return fmt.Errorf("writing hint file to pod %q: %w (stderr: %s)", podName, err, stderr.String())
	}

	r.logger.Debug("prm hint file written",
		"task_run_id", taskRunID,
		"pod", podName,
		"hint_path", hintPath,
		"bytes", len(content),
	)
	return nil
}

// cleanupHintFile removes the PRM hint file from the agent pod when the task
// run completes. It uses a short timeout and treats errors as non-fatal since
// hint cleanup must not block task completion.
func (r *Reconciler) cleanupHintFile(ctx context.Context, taskRunID string) {
	if r.restConfig == nil || r.k8sClient == nil {
		return
	}

	hintPath := r.config.PRM.HintFilePath
	if hintPath == "" {
		hintPath = "/workspace/.osmia-hint.md"
	}
	if err := validateHintPath(hintPath); err != nil {
		r.logger.Warn("skipping hint cleanup: invalid path",
			"task_run_id", taskRunID,
			"error", err,
		)
		return
	}

	r.mu.RLock()
	podName := r.podNames[taskRunID]
	r.mu.RUnlock()

	if podName == "" {
		return // pod already gone or name never resolved
	}

	req := r.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(r.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "agent",
			Command:   []string{"rm", "-f", hintPath},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
		}, k8sscheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(r.restConfig, "POST", req.URL())
	if err != nil {
		r.logger.Warn("failed to create SPDY executor for hint cleanup",
			"task_run_id", taskRunID,
			"pod", podName,
			"error", err,
		)
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	if err := executor.StreamWithContext(cleanupCtx, remotecommand.StreamOptions{
		Stderr: &stderr,
	}); err != nil {
		r.logger.Warn("hint file cleanup failed",
			"task_run_id", taskRunID,
			"pod", podName,
			"error", err,
		)
	}
}

// cleanupPodName removes the cached pod name for a completed or failed task
// run. Safe to call even when no pod name was cached.
func (r *Reconciler) cleanupPodName(taskRunID string) {
	r.mu.Lock()
	delete(r.podNames, taskRunID)
	r.mu.Unlock()
}

// launchTournament starts a competitive execution tournament by creating
// multiple candidate jobs in parallel. Each candidate runs the same ticket
// on a different engine; a judge engine later selects the best result.
func (r *Reconciler) launchTournament(ctx context.Context, ticket ticketing.Ticket) error {
	cfg := r.config.CompetitiveExecution
	candidateCount := cfg.DefaultCandidates

	// Build the ordered list of candidate engines from the selector, padding
	// with unselected engines when the chain is shorter than candidateCount.
	engineChain := r.engineSelector.SelectEngines(ticket)
	if len(engineChain) < candidateCount {
		seen := make(map[string]bool, len(engineChain))
		for _, e := range engineChain {
			seen[e] = true
		}
		for name := range r.engines {
			if !seen[name] {
				engineChain = append(engineChain, name)
			}
		}
	}
	if candidateCount > len(engineChain) {
		candidateCount = len(engineChain)
	}
	candidateEngines := engineChain[:candidateCount]

	// Cache the ticket so completion handlers can access its metadata.
	r.mu.Lock()
	r.ticketCache[ticket.ID] = ticket
	r.mu.Unlock()

	// Query episodic memory once and share the context across all candidates.
	var memoryContext string
	if r.memoryQuery != nil {
		if mc, qErr := r.memoryQuery.QueryForTask(ctx, ticket.Description, ticket.RepoURL, candidateEngines[0], ""); qErr == nil && mc != nil {
			memoryContext = mc.FormattedSection
			if mc.FormattedSection != "" {
				r.logger.InfoContext(ctx, "memory context injected into tournament prompt",
					"ticket_id", ticket.ID,
					"relevant_facts", len(mc.RelevantFacts),
					"engine_insights", len(mc.EngineInsights),
					"known_issues", len(mc.KnownIssues),
				)
			}
		}
	}

	tournamentID := fmt.Sprintf("tournament-%s-%d", ticket.ID, time.Now().UnixMilli())

	// Send start notification before building candidate specs so the returned
	// thread reference can be injected into every candidate container's env.
	tournamentThreadRef := r.runNotifyStart(ctx, ticket)

	// Create and launch a TaskRun + Job for each candidate engine.
	var candidateTaskRunIDs []string
	var createdTaskRuns []*taskrun.TaskRun

	for i, engineName := range candidateEngines {
		eng, ok := r.engines[engineName]
		if !ok {
			r.logger.WarnContext(ctx, "tournament candidate engine not registered, skipping",
				"engine", engineName,
			)
			continue
		}

		// Use the standard idempotency key for the first candidate so the
		// per-ticket idempotency check in ProcessTicket works correctly when
		// the ticket is polled again before the tournament completes.
		var idempotencyKey string
		if i == 0 {
			idempotencyKey = fmt.Sprintf("%s-1", ticket.ID)
		} else {
			idempotencyKey = fmt.Sprintf("%s-t%d", ticket.ID, i)
		}

		tr := taskrun.New(
			fmt.Sprintf("tr-%s-%s-%d", ticket.ID, engineName, time.Now().UnixMilli()),
			idempotencyKey,
			ticket.ID,
			engineName,
		)
		tr.CurrentEngine = engineName
		tr.EngineAttempts = []string{engineName}
		tr.NotificationThreadRef = tournamentThreadRef

		task := engine.Task{
			ID:            ticket.ID,
			TicketID:      ticket.ID,
			TaskRunID:     tr.ID,
			Title:         ticket.Title,
			Description:   ticket.Description,
			RepoURL:       ticket.RepoURL,
			Labels:        ticket.Labels,
			MemoryContext: memoryContext,
		}

		engineCfg := r.baseEngineConfig(engineName)
		injectThreadRef(&engineCfg, tournamentThreadRef)

		if err := r.prepareSession(ctx, tr.ID); err != nil {
			r.logger.ErrorContext(ctx, "failed to prepare session storage for tournament candidate",
				"task_run_id", tr.ID,
				"error", err,
			)
			continue
		}

		spec, err := eng.BuildExecutionSpec(task, engineCfg)
		if err != nil {
			r.logger.ErrorContext(ctx, "failed to build tournament candidate execution spec",
				"engine", engineName,
				"ticket_id", ticket.ID,
				"error", err,
			)
			continue
		}

		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to save tournament candidate task run",
				"task_run_id", tr.ID,
				"error", err,
			)
		}

		job, err := r.jobBuilder.Build(tr.ID, engineName, spec)
		if err != nil {
			r.logger.ErrorContext(ctx, "failed to build tournament candidate k8s job",
				"engine", engineName,
				"task_run_id", tr.ID,
				"error", err,
			)
			continue
		}

		if r.k8sClient != nil {
			if _, err := r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
				r.logger.ErrorContext(ctx, "failed to create tournament candidate k8s job",
					"engine", engineName,
					"task_run_id", tr.ID,
					"error", err,
				)
				continue
			}
		}

		if err := tr.Transition(taskrun.StateRunning); err != nil {
			r.logger.ErrorContext(ctx, "failed to transition tournament candidate to running",
				"task_run_id", tr.ID,
				"error", err,
			)
			continue
		}
		tr.JobName = job.Name

		candidateTaskRunIDs = append(candidateTaskRunIDs, tr.ID)
		createdTaskRuns = append(createdTaskRuns, tr)
	}

	if len(candidateTaskRunIDs) < 2 {
		return fmt.Errorf("only %d tournament candidates launched (need at least 2)", len(candidateTaskRunIDs))
	}

	// Register the tournament with the coordinator.
	tournamentCfg := tournament.TournamentConfig{
		CandidateCount:            len(candidateTaskRunIDs),
		CandidateEngines:          candidateEngines,
		JudgeEngine:               cfg.JudgeEngine,
		EarlyTerminationThreshold: cfg.EarlyTerminationThreshold,
		MaxConcurrentTournaments:  cfg.MaxConcurrentTournaments,
	}
	if _, err := r.tournamentCoordinator.StartTournament(ctx, tournamentID, ticket.ID, candidateTaskRunIDs, tournamentCfg); err != nil {
		return fmt.Errorf("starting tournament: %w", err)
	}

	// Register all task runs and their tournament roles.
	r.mu.Lock()
	for _, tr := range createdTaskRuns {
		r.taskRuns[tr.IdempotencyKey] = tr
		r.engineChains[tr.IdempotencyKey] = []string{tr.CurrentEngine}
		r.taskRunRole[tr.ID] = "candidate"
		r.taskRunToTournament[tr.ID] = tournamentID
	}
	r.mu.Unlock()

	// Persist state for all created task runs.
	for _, tr := range createdTaskRuns {
		if err := r.taskRunStore.Save(ctx, tr); err != nil {
			r.logger.ErrorContext(ctx, "failed to persist tournament candidate task run state",
				"task_run_id", tr.ID,
				"error", err,
			)
		}
	}

	metrics.ActiveJobs.Add(float64(len(createdTaskRuns)))
	for range createdTaskRuns {
		metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()
	}

	if err := r.ticketing.MarkInProgress(ctx, ticket.ID); err != nil {
		r.logger.ErrorContext(ctx, "failed to mark ticket in progress for tournament",
			"ticket_id", ticket.ID,
			"error", err,
		)
	}

	for _, tr := range createdTaskRuns {
		if tr.CurrentEngine == "claude-code" {
			r.startStreamReader(ctx, tr)
		}
	}

	r.logger.InfoContext(ctx, "tournament launched",
		"tournament_id", tournamentID,
		"ticket_id", ticket.ID,
		"candidates", len(candidateTaskRunIDs),
	)

	return nil
}

// handleCandidateComplete processes a tournament candidate that has finished
// execution. It records the result with the coordinator and, when the
// early-termination threshold is met, cancels lagging candidates and starts
// the judging phase.
func (r *Reconciler) handleCandidateComplete(ctx context.Context, tr *taskrun.TaskRun, tournamentID string) {
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateSucceeded); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition tournament candidate to succeeded",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	result := engine.TaskResult{Success: true, Summary: "candidate completed"}
	if tr.Result != nil {
		result = *tr.Result
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save tournament candidate task run",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()

	var costUSD float64
	if tr.Result != nil {
		costUSD = tr.Result.CostEstimateUSD
	}

	candidateResult := &tournament.CandidateResult{
		TaskRunID: tr.ID,
		Engine:    tr.CurrentEngine,
		Summary:   result.Summary,
		Success:   result.Success,
		Cost:      costUSD,
		Duration:  time.Since(tr.CreatedAt),
	}

	readyForJudging, err := r.tournamentCoordinator.OnCandidateComplete(ctx, tournamentID, candidateResult)
	if err != nil {
		r.logger.WarnContext(ctx, "recording tournament candidate result",
			"task_run_id", tr.ID,
			"tournament_id", tournamentID,
			"error", err,
		)
		return
	}

	r.logger.InfoContext(ctx, "tournament candidate completed",
		"task_run_id", tr.ID,
		"tournament_id", tournamentID,
		"engine", tr.CurrentEngine,
		"success", result.Success,
		"ready_for_judging", readyForJudging,
	)

	if !readyForJudging {
		return
	}

	// Cancel stream readers for candidates that haven't finished yet.
	for _, lagID := range r.tournamentCoordinator.LaggingCandidates(tournamentID) {
		r.cancelStreamReader(lagID)
	}

	r.launchJudge(ctx, tournamentID)
}

// launchJudge creates the judge job for a tournament. It atomically
// transitions the tournament to the judging phase (preventing duplicate
// judges), builds a judge prompt from completed candidates, and launches
// a K8s Job using the configured judge engine.
func (r *Reconciler) launchJudge(ctx context.Context, tournamentID string) {
	t := r.tournamentCoordinator.GetTournament(tournamentID)
	if t == nil {
		r.logger.ErrorContext(ctx, "tournament not found when launching judge",
			"tournament_id", tournamentID,
		)
		return
	}

	if len(t.CompletedResults()) < 2 {
		r.logger.WarnContext(ctx, "not enough completed candidates to judge, cancelling",
			"tournament_id", tournamentID,
			"completed", len(t.CompletedResults()),
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	// Determine the judge engine.
	judgeEngineName := t.Config.JudgeEngine
	if judgeEngineName == "" {
		for name := range r.engines {
			judgeEngineName = name
			break
		}
	}
	judgeEng, ok := r.engines[judgeEngineName]
	if !ok {
		r.logger.ErrorContext(ctx, "judge engine not registered",
			"engine", judgeEngineName,
			"tournament_id", tournamentID,
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	// Pre-generate the judge task run ID so it can be passed to BeginJudging
	// atomically, preventing any concurrent goroutine from launching a second judge.
	judgeRunID := fmt.Sprintf("tr-%s-judge-%d", t.TicketID, time.Now().UnixMilli())

	// Transition tournament to judging state. This call is atomic under the
	// coordinator's mutex, so only one goroutine succeeds.
	candidates, err := r.tournamentCoordinator.BeginJudging(ctx, tournamentID, judgeRunID)
	if err != nil {
		r.logger.WarnContext(ctx, "tournament judging already started or transition failed",
			"tournament_id", tournamentID,
			"error", err,
		)
		return
	}

	// Retrieve the task description from the ticket cache.
	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[t.TicketID]
	r.mu.RUnlock()

	taskDescription := t.TicketID // fallback to ticket ID when not cached
	if hasTicket {
		taskDescription = cachedTicket.Description
	}

	// Build the judge prompt from completed candidate results.
	promptBuilder := tournament.NewJudgePromptBuilder()
	judgePrompt, err := promptBuilder.BuildPrompt(taskDescription, candidates)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build judge prompt",
			"tournament_id", tournamentID,
			"error", err,
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	judgeTask := engine.Task{
		ID:          t.TicketID + "-judge",
		TicketID:    t.TicketID,
		Title:       "Tournament Judge: " + t.TicketID,
		Description: judgePrompt,
	}
	if hasTicket {
		judgeTask.RepoURL = cachedTicket.RepoURL
	}

	judgeEngineCfg := r.baseEngineConfig(judgeEngineName)

	spec, err := judgeEng.BuildExecutionSpec(judgeTask, judgeEngineCfg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build judge execution spec",
			"engine", judgeEngineName,
			"tournament_id", tournamentID,
			"error", err,
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	judgeTR := taskrun.New(
		judgeRunID,
		fmt.Sprintf("%s-judge", t.TicketID),
		t.TicketID,
		judgeEngineName,
	)
	judgeTR.CurrentEngine = judgeEngineName
	judgeTR.EngineAttempts = []string{judgeEngineName}

	job, err := r.jobBuilder.Build(judgeTR.ID, judgeEngineName, spec)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build judge k8s job",
			"engine", judgeEngineName,
			"task_run_id", judgeTR.ID,
			"error", err,
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	if r.k8sClient != nil {
		if _, err := r.k8sClient.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			r.logger.ErrorContext(ctx, "failed to create judge k8s job",
				"engine", judgeEngineName,
				"task_run_id", judgeTR.ID,
				"error", err,
			)
			_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
			return
		}
	}

	if err := judgeTR.Transition(taskrun.StateRunning); err != nil {
		r.logger.ErrorContext(ctx, "failed to transition judge task run to running",
			"task_run_id", judgeTR.ID,
			"error", err,
		)
		return
	}
	judgeTR.JobName = job.Name

	r.mu.Lock()
	r.taskRuns[judgeTR.IdempotencyKey] = judgeTR
	r.engineChains[judgeTR.IdempotencyKey] = []string{judgeEngineName}
	r.taskRunRole[judgeTR.ID] = "judge"
	r.taskRunToTournament[judgeTR.ID] = tournamentID
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, judgeTR); err != nil {
		r.logger.ErrorContext(ctx, "failed to save judge task run",
			"task_run_id", judgeTR.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Inc()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateRunning)).Inc()

	if judgeEngineName == "claude-code" {
		r.startStreamReader(ctx, judgeTR)
	}

	r.logger.InfoContext(ctx, "tournament judge job launched",
		"tournament_id", tournamentID,
		"judge_task_run_id", judgeTR.ID,
		"engine", judgeEngineName,
		"job", job.Name,
	)
}

// handleJudgeComplete processes a completed tournament judge run. It parses
// the JudgeDecision from the result summary (expected as a JSON object),
// records the winner with the coordinator, and completes the ticket using
// the winning candidate's result.
func (r *Reconciler) handleJudgeComplete(ctx context.Context, tr *taskrun.TaskRun, tournamentID string) {
	r.mu.Lock()
	if err := tr.Transition(taskrun.StateSucceeded); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to transition judge task run to succeeded",
			"task_run_id", tr.ID,
			"error", err,
		)
		return
	}
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save judge task run",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	metrics.ActiveJobs.Dec()
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateSucceeded)).Inc()

	t := r.tournamentCoordinator.GetTournament(tournamentID)
	if t == nil {
		r.logger.ErrorContext(ctx, "tournament not found when processing judge result",
			"tournament_id", tournamentID,
			"task_run_id", tr.ID,
		)
		return
	}

	// Parse the JudgeDecision from the result summary. The judge may embed
	// prose around the JSON block, so we extract the first {...} substring.
	summaryJSON := ""
	if tr.Result != nil {
		summaryJSON = tr.Result.Summary
	}
	if start := strings.Index(summaryJSON, "{"); start >= 0 {
		if end := strings.LastIndex(summaryJSON, "}"); end > start {
			summaryJSON = summaryJSON[start : end+1]
		}
	}

	var decision tournament.JudgeDecision
	if err := json.Unmarshal([]byte(summaryJSON), &decision); err != nil {
		r.logger.WarnContext(ctx, "failed to parse judge decision, defaulting to candidate 0",
			"tournament_id", tournamentID,
			"task_run_id", tr.ID,
			"error", err,
		)
		decision.WinnerIndex = 0
	}

	candidates := t.CompletedResults()
	if len(candidates) == 0 {
		r.logger.ErrorContext(ctx, "no candidates available for winner selection",
			"tournament_id", tournamentID,
		)
		_ = r.tournamentCoordinator.CancelTournament(ctx, tournamentID)
		return
	}

	if decision.WinnerIndex < 0 || decision.WinnerIndex >= len(candidates) {
		r.logger.WarnContext(ctx, "judge winner index out of range, defaulting to 0",
			"tournament_id", tournamentID,
			"winner_index", decision.WinnerIndex,
			"candidates", len(candidates),
		)
		decision.WinnerIndex = 0
	}

	winnerResult := candidates[decision.WinnerIndex]
	if err := r.tournamentCoordinator.SelectWinner(ctx, tournamentID, winnerResult.TaskRunID); err != nil {
		r.logger.ErrorContext(ctx, "failed to record tournament winner",
			"tournament_id", tournamentID,
			"winner_task_run_id", winnerResult.TaskRunID,
			"error", err,
		)
		return
	}

	r.logger.InfoContext(ctx, "tournament winner selected",
		"tournament_id", tournamentID,
		"winner_task_run_id", winnerResult.TaskRunID,
		"winner_engine", winnerResult.Engine,
		"reasoning", decision.Reasoning,
	)

	result := engine.TaskResult{
		Success: winnerResult.Success,
		Summary: winnerResult.Summary,
	}

	r.mu.RLock()
	cachedTicket, hasTicket := r.ticketCache[t.TicketID]
	tournamentThreadRef := r.ticketNotificationRefs[t.TicketID]
	r.mu.RUnlock()

	if r.ticketing != nil {
		if err := r.ticketing.MarkComplete(ctx, t.TicketID, result); err != nil {
			r.logger.ErrorContext(ctx, "failed to mark ticket complete after tournament",
				"ticket_id", t.TicketID,
				"error", err,
			)
		}
	}

	if hasTicket {
		for _, n := range r.notifiers {
			if err := n.NotifyComplete(ctx, cachedTicket, result, tournamentThreadRef); err != nil {
				r.logger.ErrorContext(ctx, "completion notification failed after tournament",
					"ticket_id", t.TicketID,
					"error", err,
				)
			}
		}
	}

	// Extract memory from the winning candidate's task run.
	if r.memoryExtractor != nil {
		r.mu.RLock()
		var winningTR *taskrun.TaskRun
		for _, storedTR := range r.taskRuns {
			if storedTR.ID == winnerResult.TaskRunID {
				winningTR = storedTR
				break
			}
		}
		r.mu.RUnlock()
		if winningTR != nil {
			go r.extractMemory(ctx, winningTR)
		}
	}
}
