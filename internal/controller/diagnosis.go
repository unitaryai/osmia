package controller

import (
	"context"
	"slices"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// maxDiagnosisEvents bounds how many of a task run's most recent stream
// events are kept for failure diagnosis. The analyser looks for error
// messages and tool-call oscillation near the end of a run, so the tail is
// what matters, and the bound keeps memory flat for long runs.
const maxDiagnosisEvents = 500

// diagnosisEnabled reports whether failed task runs are diagnosed.
func (r *Reconciler) diagnosisEnabled() bool {
	return r.analyser != nil && r.config != nil && r.config.Diagnosis.Enabled
}

// recordRunEvent keeps a stream event for a later failure diagnosis,
// dropping the oldest once maxDiagnosisEvents is reached.
func (r *Reconciler) recordRunEvent(taskRunID string, event *agentstream.StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runEvents == nil {
		r.runEvents = make(map[string][]*agentstream.StreamEvent)
	}
	events := append(r.runEvents[taskRunID], event)
	if len(events) > maxDiagnosisEvents {
		events = slices.Clone(events[len(events)-maxDiagnosisEvents:])
	}
	r.runEvents[taskRunID] = events
}

// takeRunEvents returns and forgets the stream events kept for a task run.
func (r *Reconciler) takeRunEvents(taskRunID string) []*agentstream.StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.runEvents[taskRunID]
	delete(r.runEvents, taskRunID)
	return events
}

// diagnoseFailure classifies why a task run failed, or returns nil when
// diagnosis is off or fails. It is called without r.mu held, since the
// analyser may call an LLM.
func (r *Reconciler) diagnoseFailure(ctx context.Context, tr *taskrun.TaskRun, reason string) *diagnosis.Diagnosis {
	events := r.takeRunEvents(tr.ID)
	if !r.diagnosisEnabled() {
		return nil
	}

	r.mu.RLock()
	result := tr.Result
	r.mu.RUnlock()

	diag, err := r.analyser.Analyse(ctx, diagnosis.DiagnosisInput{
		TaskRun:        tr,
		Events:         events,
		WatchdogReason: reason,
		Result:         result,
	})
	if err != nil || diag == nil {
		r.logger.WarnContext(ctx, "failure diagnosis did not produce a result",
			"task_run_id", tr.ID,
			"error", err,
		)
		return nil
	}

	if diag.SuggestedEngine == "" && r.config.Diagnosis.EnableEngineSwitch {
		diag.SuggestedEngine = r.suggestEngine(tr, diag.Mode)
	}
	return diag
}

// suggestEngine picks an engine to retry with after an engine-specific
// failure: the first configured engine (the default, then the configured
// fallbacks, in order) that is registered and that this task run has not
// tried. It returns "" when the failure is not engine-specific or every
// configured engine has been tried.
func (r *Reconciler) suggestEngine(tr *taskrun.TaskRun, mode diagnosis.FailureMode) string {
	if !diagnosis.IsEngineSpecific(mode) {
		return ""
	}
	candidates := append([]string{r.config.Engines.Default}, r.config.Engines.FallbackEngines...)
	for _, name := range candidates {
		if name == "" || slices.Contains(tr.EngineAttempts, name) {
			continue
		}
		if _, ok := r.engines[name]; ok {
			return name
		}
	}
	return ""
}

// isInfraFailure reports whether a diagnosis classed the failure as
// infrastructure (out of memory, timeout, network), which neither a retry
// nor another engine can fix.
func isInfraFailure(diag *diagnosis.Diagnosis) bool {
	return diag != nil && diag.Mode == diagnosis.InfraFailure
}
