//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// -----------------------------------------------------------------------------
// Fake engines
// -----------------------------------------------------------------------------

// workflowFakeEngine is a test double for engine.ExecutionEngine that runs the
// fake-agent binary with a configurable scenario. Name() returns "claude-code"
// by default (required for stream reader activation), but can be overridden via
// the name field.
type workflowFakeEngine struct {
	name     string // defaults to "claude-code" when empty
	scenario string

	mu       sync.Mutex
	lastTask engine.Task // most recent task passed to BuildPrompt
}

func (e *workflowFakeEngine) Name() string {
	if e.name != "" {
		return e.name
	}
	return "claude-code"
}

func (e *workflowFakeEngine) InterfaceVersion() int { return 1 }

// BuildExecutionSpec returns a spec that runs fake-agent with the configured
// scenario. When the task title starts with "Tournament Judge:", the scenario
// is automatically overridden to "judge" so the coordinator gets a valid winner
// JSON block in the result summary.
//
// BuildPrompt is called first (mirroring real engine implementations) so that
// lastTask is captured with the full engine.Task, including MemoryContext.
func (e *workflowFakeEngine) BuildExecutionSpec(task engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	if _, err := e.BuildPrompt(task); err != nil {
		return nil, err
	}
	scenario := e.scenario
	if strings.HasPrefix(task.Title, "Tournament Judge:") {
		scenario = "judge"
	}
	return &engine.ExecutionSpec{
		Image:                 fakeAgentImage(),
		Command:               []string{"/fake-agent"},
		Env:                   map[string]string{"OSMIA_SCENARIO": scenario},
		ActiveDeadlineSeconds: 120,
	}, nil
}

// BuildPrompt stores the last task it was called with for later assertion, and
// returns a minimal prompt string.
func (e *workflowFakeEngine) BuildPrompt(task engine.Task) (string, error) {
	e.mu.Lock()
	e.lastTask = task
	e.mu.Unlock()
	return "Fake prompt for: " + task.Title, nil
}

// getLastTask returns a snapshot of the last task passed to BuildPrompt.
func (e *workflowFakeEngine) getLastTask() engine.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTask
}

// workflowSecondEngine is a secondary engine used in tournament tests. Its
// Name() returns "fake-code" and it always uses the "tournament_b" scenario.
type workflowSecondEngine struct{}

func (e *workflowSecondEngine) Name() string          { return "fake-code" }
func (e *workflowSecondEngine) InterfaceVersion() int { return 1 }
func (e *workflowSecondEngine) BuildPrompt(_ engine.Task) (string, error) {
	return "Fake prompt (secondary engine)", nil
}
func (e *workflowSecondEngine) BuildExecutionSpec(_ engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	return &engine.ExecutionSpec{
		Image:                 fakeAgentImage(),
		Command:               []string{"/fake-agent"},
		Env:                   map[string]string{"OSMIA_SCENARIO": "tournament_b"},
		ActiveDeadlineSeconds: 120,
	}, nil
}

// -----------------------------------------------------------------------------
// Mock ticketing backend
// -----------------------------------------------------------------------------

// mockWorkflowTicketing implements ticketing.Backend for workflow E2E tests.
// It is thread-safe and tracks all calls made by the reconciler.
type mockWorkflowTicketing struct {
	mu             sync.Mutex
	tickets        []ticketing.Ticket
	pollCount      int
	maxPolls       int // return tickets for this many polls, then empty (0 = unlimited)
	markedProgress []string
	markedComplete []string
	markedFailed   []string
}

func (m *mockWorkflowTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pollCount++
	if m.maxPolls > 0 && m.pollCount > m.maxPolls {
		return nil, nil
	}
	return m.tickets, nil
}

func (m *mockWorkflowTicketing) MarkInProgress(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedProgress = append(m.markedProgress, id)
	return nil
}

func (m *mockWorkflowTicketing) MarkComplete(_ context.Context, id string, _ engine.TaskResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedComplete = append(m.markedComplete, id)
	return nil
}

func (m *mockWorkflowTicketing) MarkFailed(_ context.Context, id string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedFailed = append(m.markedFailed, id)
	return nil
}

func (m *mockWorkflowTicketing) AddComment(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockWorkflowTicketing) Name() string          { return "mock-workflow" }
func (m *mockWorkflowTicketing) InterfaceVersion() int { return ticketing.InterfaceVersion }

// -----------------------------------------------------------------------------
// testLogWriter — captures slog output for assertion
// -----------------------------------------------------------------------------

// testLogWriter is an io.Writer that collects log lines for later inspection.
type testLogWriter struct {
	mu    sync.Mutex
	lines []string
}

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.lines = append(w.lines, string(p))
	w.mu.Unlock()
	return len(p), nil
}

// hasAny returns true if any captured line contains at least one of the given
// keywords (case-insensitive).
func (w *testLogWriter) hasAny(keywords ...string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range w.lines {
		lower := strings.ToLower(line)
		for _, kw := range keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return true
			}
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Helper functions
// -----------------------------------------------------------------------------

// fakeAgentImage returns the container image for the fake agent, read from the
// FAKE_AGENT_IMAGE environment variable (default: "fake-agent:latest").
func fakeAgentImage() string {
	if img := os.Getenv("FAKE_AGENT_IMAGE"); img != "" {
		return img
	}
	return "fake-agent:e2e"
}

// workflowNamespace returns the namespace for workflow E2E tests.
func workflowNamespace() string {
	if ns := os.Getenv("OSMIA_WORKFLOW_NAMESPACE"); ns != "" {
		return ns
	}
	return "osmia-e2e-workflow"
}

// workflowConfig returns a base controller config suitable for workflow tests.
func workflowConfig(ns string) *config.Config {
	_ = ns // namespace is used elsewhere; kept for symmetry with other helpers
	return &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     10,
			MaxJobDurationMinutes: 10,
			AllowedRepos:          []string{"https://github.com/*"},
			AllowedTaskTypes:      []string{"issue"},
		},
	}
}

// workflowTicket returns a ticket that satisfies the default guard rails.
func workflowTicket(id string) ticketing.Ticket {
	return ticketing.Ticket{
		ID:          id,
		Title:       "Workflow test ticket " + id,
		Description: "E2E workflow test ticket",
		TicketType:  "issue",
		RepoURL:     "https://github.com/org/repo",
		Labels:      []string{"osmia"},
	}
}

// newRestConfig loads the Kubernetes REST config from the current kubeconfig.
func newRestConfig(t *testing.T) *rest.Config {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		t.Fatalf("failed to load kubeconfig for rest config: %v", err)
	}
	return cfg
}

// ensureNamespace creates the namespace if it does not already exist.
func ensureNamespace(t *testing.T, k8s kubernetes.Interface, ns string) {
	t.Helper()
	_, err := k8s.CoreV1().Namespaces().Create(
		context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		metav1.CreateOptions{},
	)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create namespace %q: %v", ns, err)
	}
}

// newWorkflowReconciler builds a Reconciler wired for workflow E2E tests.
// The primary engine eng is registered; additional options (e.g. secondary
// engines, watchdog, memory) can be supplied via opts.
func newWorkflowReconciler(
	t *testing.T,
	k8s kubernetes.Interface,
	restCfg *rest.Config,
	ns string,
	mock *mockWorkflowTicketing,
	eng engine.ExecutionEngine,
	opts ...controller.ReconcilerOption,
) *controller.Reconciler {
	t.Helper()
	cfg := workflowConfig(ns)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jb := jobbuilder.NewJobBuilder(ns)

	baseOpts := []controller.ReconcilerOption{
		controller.WithTicketing(mock),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace(ns),
	}
	if restCfg != nil {
		baseOpts = append(baseOpts, controller.WithRestConfig(restCfg))
	}
	baseOpts = append(baseOpts, opts...)

	return controller.NewReconciler(cfg, logger, baseOpts...)
}

// runReconcilerInBackground starts the reconciler in a background goroutine.
// A child context is created and its cancel function is registered with
// t.Cleanup so the reconciler stops when the test ends.
func runReconcilerInBackground(t *testing.T, r *controller.Reconciler, ctx context.Context) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = r.Run(runCtx, 500*time.Millisecond) }()
}

// waitForTicketComplete polls until id appears in mock.markedComplete or
// timeout elapses. It calls t.Fatal on timeout.
func waitForTicketComplete(t *testing.T, mock *mockWorkflowTicketing, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		for _, completed := range mock.markedComplete {
			if completed == id {
				mock.mu.Unlock()
				return
			}
		}
		mock.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for ticket %q to be marked complete", timeout, id)
}

// waitForTicketFailed polls until id appears in mock.markedFailed or
// timeout elapses. It calls t.Fatal on timeout.
func waitForTicketFailed(t *testing.T, mock *mockWorkflowTicketing, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		for _, failed := range mock.markedFailed {
			if failed == id {
				mock.mu.Unlock()
				return
			}
		}
		mock.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for ticket %q to be marked failed", timeout, id)
}
