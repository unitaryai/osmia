// Package watchdog implements the progress watchdog loop that detects
// stalled, looping, or otherwise unproductive agents during execution.
package watchdog

import (
	"context"
	"log/slog"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// Action represents the action the watchdog recommends when an anomaly is detected.
type Action string

const (
	// ActionTerminate kills the job immediately.
	ActionTerminate Action = "terminate"
	// ActionTerminateWithFeedback kills the job and appends diagnostic feedback for retry.
	ActionTerminateWithFeedback Action = "terminate_with_feedback"
	// ActionTerminateAndNotify kills the job and notifies the human.
	ActionTerminateAndNotify Action = "terminate_and_notify"
	// ActionWarn logs a warning and sets a condition on the TaskRun.
	ActionWarn Action = "warn"
)

// Heartbeat represents enriched telemetry data pushed from an agent container.
type Heartbeat struct {
	Seq                       int64      `json:"seq"`
	RunID                     string     `json:"run_id"`
	Timestamp                 time.Time  `json:"timestamp"`
	TokensConsumed            int64      `json:"tokens_consumed"`
	FilesChanged              int        `json:"files_changed"`
	ToolCallsTotal            int        `json:"tool_calls_total"`
	LastToolName              string     `json:"last_tool_name,omitempty"`
	LastToolArgsHash          string     `json:"last_tool_args_hash,omitempty"`
	ConsecutiveIdenticalCalls int        `json:"consecutive_identical_calls"`
	LastMeaningfulChangeAt    *time.Time `json:"last_meaningful_change_at,omitempty"`
	CostEstimateUSD           float64    `json:"cost_estimate_usd"`
}

// Reason provides structured diagnostic information when the watchdog
// detects an anomaly. Fields are populated from templates, never from
// raw agent output, to prevent prompt injection.
type Reason struct {
	ReasonCode      string  `json:"reason_code"`
	ToolName        string  `json:"tool_name,omitempty"`
	CallCount       int     `json:"call_count,omitempty"`
	TokensConsumed  int64   `json:"tokens_consumed"`
	CostEstimateUSD float64 `json:"cost_estimate_usd"`
	Message         string  `json:"message"`
}

// LoopDetectionConfig configures the loop detection rule.
type LoopDetectionConfig struct {
	ConsecutiveIdenticalCallThreshold int    `yaml:"consecutive_identical_call_threshold"`
	RequireNoFileProgress             bool   `yaml:"require_no_file_progress"`
	Action                            Action `yaml:"action"`
}

// ThrashingDetectionConfig configures the thrashing detection rule.
type ThrashingDetectionConfig struct {
	TokensWithoutProgressThreshold int64  `yaml:"tokens_without_progress_threshold"`
	Action                         Action `yaml:"action"`
	EscalationAction               Action `yaml:"escalation_action"`
}

// StallDetectionConfig configures the stall detection rule.
type StallDetectionConfig struct {
	IdleSecondsThreshold int    `yaml:"idle_seconds_threshold"`
	Action               Action `yaml:"action"`
}

// CostVelocityConfig configures the cost velocity rule.
type CostVelocityConfig struct {
	MaxUSDPer10Minutes float64 `yaml:"max_usd_per_10_minutes"`
	Action             Action  `yaml:"action"`
}

// TelemetryFailureConfig configures the telemetry failure rule.
type TelemetryFailureConfig struct {
	StaleTicksThreshold int    `yaml:"stale_ticks_threshold"`
	Action              Action `yaml:"action"`
}

// RulesConfig holds all anomaly detection rule configurations.
type RulesConfig struct {
	LoopDetection                 LoopDetectionConfig      `yaml:"loop_detection"`
	ThrashingDetection            ThrashingDetectionConfig `yaml:"thrashing_detection"`
	StallDetection                StallDetectionConfig     `yaml:"stall_detection"`
	CostVelocity                  CostVelocityConfig       `yaml:"cost_velocity"`
	TelemetryFailure              TelemetryFailureConfig   `yaml:"telemetry_failure"`
	MaxCostPerJob                 float64                  `yaml:"max_cost_per_job"`
	UnansweredHumanTimeoutMinutes int                      `yaml:"unanswered_human_timeout_minutes"`
	UnansweredHumanAction         Action                   `yaml:"unanswered_human_action"`
}

// AdaptiveCalibrationConfig configures the adaptive calibration system
// that learns thresholds from historical TaskRun data.
type AdaptiveCalibrationConfig struct {
	Enabled             bool   `yaml:"enabled"`
	MinSampleCount      int    `yaml:"min_sample_count"`
	PercentileThreshold string `yaml:"percentile_threshold"` // "p50", "p90", or "p99"
	ColdStartFallback   bool   `yaml:"cold_start_fallback"`
}

// DefaultAdaptiveCalibrationConfig returns conservative defaults for adaptive calibration.
func DefaultAdaptiveCalibrationConfig() AdaptiveCalibrationConfig {
	return AdaptiveCalibrationConfig{
		Enabled:             false,
		MinSampleCount:      10,
		PercentileThreshold: "p90",
		ColdStartFallback:   true,
	}
}

// Config holds the top-level watchdog configuration.
type Config struct {
	CheckIntervalSeconds       int                       `yaml:"check_interval_seconds"`
	MinConsecutiveTicks        int                       `yaml:"min_consecutive_ticks"`
	ResearchGracePeriodMinutes int                       `yaml:"research_grace_period_minutes"`
	Rules                      RulesConfig               `yaml:"rules"`
	AdaptiveCalibration        AdaptiveCalibrationConfig `yaml:"adaptive_calibration"`
}

// DefaultConfig returns a Config with conservative default values
// matching the specification in the plan document.
func DefaultConfig() Config {
	return Config{
		CheckIntervalSeconds:       60,
		MinConsecutiveTicks:        2,
		ResearchGracePeriodMinutes: 5,
		Rules: RulesConfig{
			LoopDetection: LoopDetectionConfig{
				ConsecutiveIdenticalCallThreshold: 10,
				RequireNoFileProgress:             true,
				Action:                            ActionTerminateWithFeedback,
			},
			ThrashingDetection: ThrashingDetectionConfig{
				TokensWithoutProgressThreshold: 80000,
				Action:                         ActionWarn,
				EscalationAction:               ActionTerminateWithFeedback,
			},
			StallDetection: StallDetectionConfig{
				IdleSecondsThreshold: 300,
				Action:               ActionTerminate,
			},
			CostVelocity: CostVelocityConfig{
				MaxUSDPer10Minutes: 15.00,
				Action:             ActionWarn,
			},
			TelemetryFailure: TelemetryFailureConfig{
				StaleTicksThreshold: 3,
				Action:              ActionWarn,
			},
			UnansweredHumanTimeoutMinutes: 30,
			UnansweredHumanAction:         ActionTerminateAndNotify,
		},
	}
}

// tickState tracks consecutive anomaly ticks for a single TaskRun.
type tickState struct {
	consecutiveTicks int
	lastReasonCode   string
}

// CalibrationOverrideHook is called whenever calibrated thresholds override
// static defaults. This allows the caller (e.g. the controller) to update
// Prometheus metrics without creating an import cycle.
type CalibrationOverrideHook func()

// Watchdog monitors active TaskRuns for anomalous behaviour and recommends
// corrective actions when agents are stalled, looping, or unproductive.
type Watchdog struct {
	config                  Config
	logger                  *slog.Logger
	ticks                   map[string]*tickState // keyed by TaskRun ID
	calibrator              *Calibrator
	profileResolver         *ProfileResolver
	calibrationOverrideHook CalibrationOverrideHook
}

// New creates a new Watchdog with the given configuration and logger.
func New(cfg Config, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		config: cfg,
		logger: logger,
		ticks:  make(map[string]*tickState),
	}
}

// NewWithCalibration creates a Watchdog with adaptive calibration support.
// The calibrator and profile resolver are used to look up calibrated
// thresholds, falling back to static config when insufficient data exists.
// The optional overrideHook is called each time calibrated thresholds are
// used instead of static defaults, enabling the caller to update metrics.
func NewWithCalibration(cfg Config, logger *slog.Logger, cal *Calibrator, resolver *ProfileResolver, overrideHook ...CalibrationOverrideHook) *Watchdog {
	var hook CalibrationOverrideHook
	if len(overrideHook) > 0 {
		hook = overrideHook[0]
	}
	return &Watchdog{
		config:                  cfg,
		logger:                  logger,
		ticks:                   make(map[string]*tickState),
		calibrator:              cal,
		profileResolver:         resolver,
		calibrationOverrideHook: hook,
	}
}

// Check evaluates the watchdog rules for a single TaskRun, comparing the
// current heartbeat against the previous one. It returns a Reason and Action
// if an anomaly is detected that has persisted for the required number of
// consecutive ticks, or nil values if no action is needed.
func (w *Watchdog) Check(
	tr *taskrun.TaskRun,
	current *Heartbeat,
	previous *Heartbeat,
) (*Reason, Action, error) {
	if tr == nil || current == nil {
		return nil, "", nil
	}

	// Evaluate rules based on current TaskRun state.
	reason, action := w.evaluateRules(tr, current, previous)
	if reason == nil {
		// No anomaly detected — reset consecutive tick counter.
		w.resetTicks(tr.ID)
		return nil, "", nil
	}

	// Track consecutive ticks for this anomaly.
	ts := w.getOrCreateTickState(tr.ID)
	if ts.lastReasonCode == reason.ReasonCode {
		ts.consecutiveTicks++
	} else {
		ts.consecutiveTicks = 1
		ts.lastReasonCode = reason.ReasonCode
	}

	// Only act if the anomaly persists for the required number of consecutive ticks.
	if ts.consecutiveTicks < w.config.MinConsecutiveTicks {
		w.logger.Info("anomaly detected but below consecutive tick threshold",
			"task_run_id", tr.ID,
			"reason", reason.ReasonCode,
			"ticks", ts.consecutiveTicks,
			"required", w.config.MinConsecutiveTicks,
		)
		return nil, "", nil
	}

	w.logger.Warn("watchdog anomaly confirmed",
		"task_run_id", tr.ID,
		"reason", reason.ReasonCode,
		"action", action,
		"ticks", ts.consecutiveTicks,
	)

	return reason, action, nil
}

// Start runs the watchdog check loop at the configured interval until the
// context is cancelled. The checkFn callback is invoked on each tick to
// perform the actual checks against active TaskRuns.
func (w *Watchdog) Start(ctx context.Context, checkFn func(ctx context.Context)) {
	interval := time.Duration(w.config.CheckIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.logger.Info("watchdog started", "interval_seconds", w.config.CheckIntervalSeconds)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watchdog stopped")
			return
		case <-ticker.C:
			checkFn(ctx)
		}
	}
}

// evaluateRules checks all applicable rules for the given TaskRun state.
func (w *Watchdog) evaluateRules(
	tr *taskrun.TaskRun,
	current *Heartbeat,
	previous *Heartbeat,
) (*Reason, Action) {
	switch tr.State {
	case taskrun.StateRunning:
		return w.evaluateRunningRules(tr, current, previous)
	case taskrun.StateNeedsHuman:
		return w.evaluateNeedsHumanRules(tr)
	default:
		return nil, ""
	}
}

// evaluateRunningRules checks rules that apply when a TaskRun is in Running state.
func (w *Watchdog) evaluateRunningRules(
	tr *taskrun.TaskRun,
	current *Heartbeat,
	previous *Heartbeat,
) (*Reason, Action) {
	rules := w.resolveRules(tr)
	inGracePeriod := w.isInGracePeriod(tr)

	// Loop detection: agent calling the same tool with same args repeatedly.
	if reason, action := w.checkLoopDetection(current, previous, rules.LoopDetection); reason != nil {
		return reason, action
	}

	// Thrashing detection: high token consumption with no file progress.
	// Relaxed during grace period.
	if !inGracePeriod {
		if reason, action := w.checkThrashingDetection(tr, current, previous, rules.ThrashingDetection); reason != nil {
			return reason, action
		}
	}

	// Stall detection: no tool calls despite heartbeat advancing.
	if previous != nil {
		if reason, action := w.checkStallDetection(current, previous, rules.StallDetection); reason != nil {
			return reason, action
		}
	}

	// Cost velocity: spending too fast.
	if previous != nil {
		if reason, action := w.checkCostVelocity(current, previous, rules.CostVelocity); reason != nil {
			return reason, action
		}
	}

	// Telemetry failure: heartbeat seq not advancing.
	if previous != nil {
		if reason, action := w.checkTelemetryFailure(current, previous, rules.TelemetryFailure); reason != nil {
			return reason, action
		}
	}

	// Total cost check: cumulative spending beyond budget.
	if reason, action := w.checkTotalCost(current, rules.MaxCostPerJob); reason != nil {
		return reason, action
	}

	return nil, ""
}

// resolveRules returns the effective RulesConfig for a TaskRun, applying
// calibrated overrides from the adaptive calibration system when enabled
// and sufficient data is available. Falls back to static config otherwise.
func (w *Watchdog) resolveRules(tr *taskrun.TaskRun) RulesConfig {
	rules := w.config.Rules

	if !w.config.AdaptiveCalibration.Enabled || w.profileResolver == nil {
		return rules
	}

	ctx := context.Background()
	profile := w.profileResolver.ResolveProfile(ctx, tr.TicketID, tr.CurrentEngine, "")
	if profile == nil {
		return rules
	}

	threshold := w.config.AdaptiveCalibration.PercentileThreshold
	overridden := false

	// Override loop detection threshold from calibrated consecutive identical calls.
	if p, ok := profile.Thresholds[SignalConsecutiveIdenticalCalls]; ok {
		val := pickPercentile(p, threshold)
		if val > 0 {
			rules.LoopDetection.ConsecutiveIdenticalCallThreshold = int(val)
			overridden = true
		}
	}

	// Override thrashing threshold from calibrated token rate.
	if p, ok := profile.Thresholds[SignalTokenRate]; ok {
		val := pickPercentile(p, threshold)
		if val > 0 {
			rules.ThrashingDetection.TokensWithoutProgressThreshold = int64(val)
			overridden = true
		}
	}

	// Override cost velocity from calibrated cost velocity signal.
	if p, ok := profile.Thresholds[SignalCostVelocity]; ok {
		val := pickPercentile(p, threshold)
		if val > 0 {
			rules.CostVelocity.MaxUSDPer10Minutes = val
			overridden = true
		}
	}

	if overridden {
		if w.calibrationOverrideHook != nil {
			w.calibrationOverrideHook()
		}
		w.logger.Debug("using calibrated thresholds",
			"task_run_id", tr.ID,
			"engine", tr.CurrentEngine,
			"sample_count", profile.SampleCount,
		)
	}

	return rules
}

// pickPercentile selects the appropriate percentile value based on the
// configured threshold string ("p50", "p90", or "p99").
func pickPercentile(p *Percentiles, threshold string) float64 {
	switch threshold {
	case "p50":
		return p.P50
	case "p99":
		return p.P99
	default:
		return p.P90
	}
}

// evaluateNeedsHumanRules checks rules that apply when a TaskRun is in NeedsHuman state.
func (w *Watchdog) evaluateNeedsHumanRules(tr *taskrun.TaskRun) (*Reason, Action) {
	rules := w.config.Rules
	if rules.UnansweredHumanTimeoutMinutes <= 0 {
		return nil, ""
	}

	timeout := time.Duration(rules.UnansweredHumanTimeoutMinutes) * time.Minute
	if time.Since(tr.UpdatedAt) > timeout {
		return &Reason{
			ReasonCode: "unanswered_human",
			Message:    "human question has not been answered within the configured timeout",
		}, rules.UnansweredHumanAction
	}

	return nil, ""
}

// checkLoopDetection detects agents repeatedly calling the same tool.
func (w *Watchdog) checkLoopDetection(
	current *Heartbeat,
	previous *Heartbeat,
	cfg LoopDetectionConfig,
) (*Reason, Action) {
	if cfg.ConsecutiveIdenticalCallThreshold <= 0 {
		return nil, ""
	}

	if current.ConsecutiveIdenticalCalls < cfg.ConsecutiveIdenticalCallThreshold {
		return nil, ""
	}

	// If we require no file progress, check that files haven't changed.
	if cfg.RequireNoFileProgress && previous != nil {
		if current.FilesChanged > previous.FilesChanged {
			return nil, ""
		}
	}

	return &Reason{
		ReasonCode:      "loop",
		ToolName:        current.LastToolName,
		CallCount:       current.ConsecutiveIdenticalCalls,
		TokensConsumed:  current.TokensConsumed,
		CostEstimateUSD: current.CostEstimateUSD,
		Message:         "agent is looping: same tool called repeatedly without progress",
	}, cfg.Action
}

// checkThrashingDetection detects high token consumption without file changes.
func (w *Watchdog) checkThrashingDetection(
	tr *taskrun.TaskRun,
	current *Heartbeat,
	previous *Heartbeat,
	cfg ThrashingDetectionConfig,
) (*Reason, Action) {
	if cfg.TokensWithoutProgressThreshold <= 0 {
		return nil, ""
	}

	// Check if tokens have been consumed without file changes.
	var tokensSinceProgress int64
	if previous != nil && current.FilesChanged <= previous.FilesChanged {
		tokensSinceProgress = current.TokensConsumed - previous.TokensConsumed
	} else if previous == nil && current.FilesChanged == 0 {
		tokensSinceProgress = current.TokensConsumed
	}

	if tokensSinceProgress < cfg.TokensWithoutProgressThreshold {
		return nil, ""
	}

	// Check if we should escalate. Escalation only fires after a warn
	// has already been emitted (i.e. ticks already met the minimum threshold).
	ts := w.getOrCreateTickState(tr.ID)
	action := cfg.Action
	if ts.lastReasonCode == "thrashing" && ts.consecutiveTicks >= w.config.MinConsecutiveTicks {
		action = cfg.EscalationAction
	}

	return &Reason{
		ReasonCode:      "thrashing",
		TokensConsumed:  current.TokensConsumed,
		CostEstimateUSD: current.CostEstimateUSD,
		Message:         "high token consumption without meaningful file changes",
	}, action
}

// checkStallDetection detects agents that have stopped making tool calls.
func (w *Watchdog) checkStallDetection(
	current *Heartbeat,
	previous *Heartbeat,
	cfg StallDetectionConfig,
) (*Reason, Action) {
	if cfg.IdleSecondsThreshold <= 0 {
		return nil, ""
	}

	// If tool calls haven't advanced since the last heartbeat,
	// check how long since the last heartbeat.
	if current.ToolCallsTotal <= previous.ToolCallsTotal {
		idleDuration := current.Timestamp.Sub(previous.Timestamp)
		if idleDuration >= time.Duration(cfg.IdleSecondsThreshold)*time.Second {
			return &Reason{
				ReasonCode:      "stall",
				TokensConsumed:  current.TokensConsumed,
				CostEstimateUSD: current.CostEstimateUSD,
				Message:         "agent has stalled: no tool calls despite heartbeat advancing",
			}, cfg.Action
		}
	}

	return nil, ""
}

// checkCostVelocity detects excessive spending rate.
func (w *Watchdog) checkCostVelocity(
	current *Heartbeat,
	previous *Heartbeat,
	cfg CostVelocityConfig,
) (*Reason, Action) {
	if cfg.MaxUSDPer10Minutes <= 0 {
		return nil, ""
	}

	elapsed := current.Timestamp.Sub(previous.Timestamp)
	if elapsed <= 0 {
		return nil, ""
	}

	costDelta := current.CostEstimateUSD - previous.CostEstimateUSD
	// Normalise to a 10-minute window.
	costPer10Min := costDelta / elapsed.Minutes() * 10

	if costPer10Min > cfg.MaxUSDPer10Minutes {
		return &Reason{
			ReasonCode:      "cost_velocity",
			TokensConsumed:  current.TokensConsumed,
			CostEstimateUSD: current.CostEstimateUSD,
			Message:         "cost velocity exceeds configured threshold",
		}, cfg.Action
	}

	return nil, ""
}

// checkTelemetryFailure detects when the heartbeat seq stops advancing.
func (w *Watchdog) checkTelemetryFailure(
	current *Heartbeat,
	previous *Heartbeat,
	cfg TelemetryFailureConfig,
) (*Reason, Action) {
	if cfg.StaleTicksThreshold <= 0 {
		return nil, ""
	}

	if current.Seq <= previous.Seq {
		return &Reason{
			ReasonCode:      "telemetry_failure",
			TokensConsumed:  current.TokensConsumed,
			CostEstimateUSD: current.CostEstimateUSD,
			Message:         "heartbeat sequence number has not advanced",
		}, cfg.Action
	}

	return nil, ""
}

// checkTotalCost detects when cumulative job cost exceeds the configured maximum.
func (w *Watchdog) checkTotalCost(current *Heartbeat, maxCost float64) (*Reason, Action) {
	if maxCost <= 0 {
		return nil, ""
	}

	if current.CostEstimateUSD > maxCost {
		return &Reason{
			ReasonCode:      "total_cost",
			TokensConsumed:  current.TokensConsumed,
			CostEstimateUSD: current.CostEstimateUSD,
			Message:         "cumulative job cost exceeds configured maximum",
		}, ActionTerminate
	}

	return nil, ""
}

// isInGracePeriod returns true if the TaskRun is still within its initial
// research grace period.
func (w *Watchdog) isInGracePeriod(tr *taskrun.TaskRun) bool {
	grace := time.Duration(w.config.ResearchGracePeriodMinutes) * time.Minute
	return time.Since(tr.CreatedAt) < grace
}

// ConsumeStreamEvent updates the TaskRun's telemetry fields based on a
// parsed stream event from the agent. This provides a real-time signal
// alongside the periodic heartbeat checks.
func (w *Watchdog) ConsumeStreamEvent(tr *taskrun.TaskRun, event *agentstream.StreamEvent) {
	if tr == nil || event == nil {
		return
	}

	switch ev := event.Parsed.(type) {
	case *agentstream.ToolCallEvent:
		tr.ToolCallsTotal++
		if tr.LastToolName == ev.Tool {
			tr.ConsecutiveIdenticalTools++
		} else {
			tr.ConsecutiveIdenticalTools = 1
			tr.LastToolName = ev.Tool
		}
		w.logger.Debug("stream: tool call consumed",
			"task_run_id", tr.ID,
			"tool", ev.Tool,
			"consecutive", tr.ConsecutiveIdenticalTools,
		)

	case *agentstream.CostEvent:
		tr.TokensConsumed = ev.InputTokens + ev.OutputTokens
		tr.CostUSD = ev.CostUSD
		w.logger.Debug("stream: cost update consumed",
			"task_run_id", tr.ID,
			"input_tokens", ev.InputTokens,
			"output_tokens", ev.OutputTokens,
			"cost_usd", ev.CostUSD,
		)

	case *agentstream.ContentDeltaEvent:
		now := time.Now()
		tr.HeartbeatAt = &now
		w.logger.Debug("stream: heartbeat updated from content delta",
			"task_run_id", tr.ID,
		)

	case *agentstream.ResultEvent:
		w.logger.Info("stream: agent result received",
			"task_run_id", tr.ID,
			"success", ev.Success,
			"summary", ev.Summary,
		)
	}
}

func (w *Watchdog) getOrCreateTickState(taskRunID string) *tickState {
	ts, ok := w.ticks[taskRunID]
	if !ok {
		ts = &tickState{}
		w.ticks[taskRunID] = ts
	}
	return ts
}

func (w *Watchdog) resetTicks(taskRunID string) {
	delete(w.ticks, taskRunID)
}
