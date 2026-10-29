package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// diagnosisReconciler builds a Reconciler with diagnosis enabled, the given
// engine chain for tr, and codex configured as a fallback engine.
func diagnosisReconciler(tr *taskrun.TaskRun, chain []string, engineSwitch bool) (*Reconciler, *mockTicketing) {
	cfg := testConfig()
	cfg.Engines.FallbackEngines = []string{"codex"}
	cfg.Diagnosis = config.DiagnosisConfig{Enabled: true, EnableEngineSwitch: engineSwitch}
	tb := newMockTicketing(nil)
	r := &Reconciler{
		config:    cfg,
		logger:    testLogger(),
		ticketing: tb,
		engines: map[string]engine.ExecutionEngine{
			"claude-code": &mockEngine{name: "claude-code"},
			"codex":       &mockEngine{name: "codex"},
		},
		taskRuns:     map[string]*taskrun.TaskRun{tr.IdempotencyKey: tr},
		engineChains: map[string][]string{tr.IdempotencyKey: chain},
		ticketCache:  map[string]ticketing.Ticket{tr.TicketID: {ID: tr.TicketID, Description: "fix it"}},
		taskRunStore: taskrun.NewMemoryStore(),
		analyser:     diagnosis.NewAnalyser(testLogger()),
		retryBuilder: diagnosis.NewRetryBuilder(testLogger()),
	}
	return r, tb
}

func runningTaskRun() *taskrun.TaskRun {
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"
	tr.EngineAttempts = []string{"claude-code"}
	_ = tr.Transition(taskrun.StateRunning)
	return tr
}

// TestHandleJobFailed_InfraFailureSkipsFallback checks that an
// infrastructure failure ends the run instead of starting the next engine
// in the chain, which would meet the same infrastructure.
func TestHandleJobFailed_InfraFailureSkipsFallback(t *testing.T) {
	tr := runningTaskRun()
	r, tb := diagnosisReconciler(tr, []string{"claude-code", "codex"}, false)

	r.handleJobFailed(context.Background(), tr, "pod OOMKilled")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Equal(t, "claude-code", tr.CurrentEngine, "must not move to the fallback engine")
	assert.Equal(t, []string{"claude-code"}, tr.EngineAttempts)
	assert.Contains(t, tb.markedFailed, "TICKET-1")
}

// TestHandleJobFailed_NonInfraFailureStillFallsBack checks the fallback
// chain is still used for failures that another engine might fix.
func TestHandleJobFailed_NonInfraFailureStillFallsBack(t *testing.T) {
	tr := runningTaskRun()
	r, tb := diagnosisReconciler(tr, []string{"claude-code", "codex"}, false)

	r.handleJobFailed(context.Background(), tr, "tests did not pass")

	assert.Equal(t, taskrun.StateRetrying, tr.State)
	assert.Equal(t, "codex", tr.CurrentEngine)
	assert.Empty(t, tb.markedFailed)
}

// TestHandleJobFailed_DiagnosisUsesStreamEvents checks the agent's recorded
// tool calls reach the analyser: an oscillating run is only recognisable as
// ModelConfusion from its events. With engine switching on, the retry then
// moves to an untried configured engine.
func TestHandleJobFailed_DiagnosisUsesStreamEvents(t *testing.T) {
	tr := runningTaskRun()
	r, _ := diagnosisReconciler(tr, []string{"claude-code"}, true)

	tools := []string{"Edit", "Bash"}
	for i := 0; i < 12; i++ {
		r.recordRunEvent(tr.ID, &agentstream.StreamEvent{
			Type:   agentstream.EventToolCall,
			Parsed: &agentstream.ToolCallEvent{Tool: tools[i%2]},
		})
	}

	r.handleJobFailed(context.Background(), tr, "agent gave up")

	require.Len(t, tr.DiagnosisHistory, 1)
	assert.Equal(t, string(diagnosis.ModelConfusion), string(tr.DiagnosisHistory[0].Mode))
	assert.Equal(t, taskrun.StateRetrying, tr.State)
	assert.Equal(t, "codex", tr.CurrentEngine, "engine-specific failure should switch engine")
	assert.Empty(t, r.runEvents, "events are released once diagnosed")
}

func TestRecordRunEvent_KeepsMostRecent(t *testing.T) {
	r := &Reconciler{}
	for i := 0; i < maxDiagnosisEvents+10; i++ {
		r.recordRunEvent("tr-1", &agentstream.StreamEvent{Raw: []byte{byte(i % 256)}})
	}
	events := r.takeRunEvents("tr-1")
	require.Len(t, events, maxDiagnosisEvents)
	assert.Equal(t, []byte{10}, []byte(events[0].Raw), "the oldest events are dropped")
	assert.Empty(t, r.takeRunEvents("tr-1"))
}

func TestSuggestEngine(t *testing.T) {
	tests := []struct {
		name     string
		mode     diagnosis.FailureMode
		attempts []string
		want     string
	}{
		{name: "engine-specific picks an untried fallback", mode: diagnosis.ModelConfusion, attempts: []string{"claude-code"}, want: "codex"},
		{name: "engine-specific with every engine tried", mode: diagnosis.ModelConfusion, attempts: []string{"claude-code", "codex"}, want: ""},
		{name: "not engine-specific", mode: diagnosis.WrongApproach, attempts: []string{"claude-code"}, want: ""},
		{name: "untried default comes first", mode: diagnosis.ModelConfusion, attempts: []string{"codex"}, want: "claude-code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := runningTaskRun()
			tr.EngineAttempts = tt.attempts
			r, _ := diagnosisReconciler(tr, nil, true)
			assert.Equal(t, tt.want, r.suggestEngine(tr, tt.mode))
		})
	}
}

func TestSuggestEngine_SkipsUnregisteredEngines(t *testing.T) {
	tr := runningTaskRun()
	r, _ := diagnosisReconciler(tr, nil, true)
	r.config.Engines.FallbackEngines = []string{"aider", "codex"}
	assert.Equal(t, "codex", r.suggestEngine(tr, diagnosis.ModelConfusion))
}
