//go:build integration

// Package integration_test contains integration tests verifying that the PRM
// evaluator is correctly wired into the controller's reconciliation pipeline.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/controller"
	"github.com/unitaryai/robodev/internal/prm"
	"github.com/unitaryai/robodev/internal/taskrun"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

func prmControllerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestPRMControllerWiring verifies that creating a Reconciler with PRM
// enabled does not break normal ticket processing. The PRM evaluator is
// only active during streaming, but the option should be accepted without
// error.
func TestPRMControllerWiring(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
		Streaming: config.StreamingConfig{Enabled: true},
	}
	logger := prmControllerLogger()
	k8s := fake.NewSimpleClientset()

	prmCfg := prm.Config{
		Enabled:                true,
		EvaluationInterval:     3,
		WindowSize:             10,
		ScoreThresholdNudge:    7,
		ScoreThresholdEscalate: 3,
		HintFilePath:           "/workspace/.robodev-hint.md",
		MaxTrajectoryLength:    50,
	}

	eng := &stubEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithPRMConfig(prmCfg),
	)

	ticket := ticketing.Ticket{
		ID:          "PRM-TICKET-1",
		Title:       "Fix login bug",
		Description: "Users cannot log in",
		RepoURL:     "https://github.com/org/repo",
	}

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("PRM-TICKET-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.NotEmpty(t, tr.JobName)
}

// TestPRMDisabledDoesNotInterfere verifies that PRM disabled (the default)
// does not change controller behaviour.
func TestPRMDisabledDoesNotInterfere(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}
	logger := prmControllerLogger()
	k8s := fake.NewSimpleClientset()

	// Explicitly pass disabled PRM.
	prmCfg := prm.Config{Enabled: false}

	eng := &stubEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithPRMConfig(prmCfg),
	)

	ticket := ticketing.Ticket{
		ID:    "PRM-OFF-1",
		Title: "Test with PRM off",
	}

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("PRM-OFF-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
}

// --- Stub types for integration tests ---

type stubEngine struct {
	name string
}

func (e *stubEngine) BuildExecutionSpec(_ engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	return &engine.ExecutionSpec{
		Image:                 "test-image:latest",
		Command:               []string{"echo", "hello"},
		Env:                   map[string]string{"TEST": "true"},
		ActiveDeadlineSeconds: 3600,
	}, nil
}

func (e *stubEngine) BuildPrompt(task engine.Task) (string, error) {
	return "test prompt for " + task.Title, nil
}

func (e *stubEngine) Name() string          { return e.name }
func (e *stubEngine) InterfaceVersion() int { return 1 }

type stubTicketing struct {
	tickets        []ticketing.Ticket
	markedProgress []string
	markedComplete []string
	markedFailed   []string
}

func newStubTicketing(tickets []ticketing.Ticket) *stubTicketing {
	return &stubTicketing{tickets: tickets}
}

func (t *stubTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	return t.tickets, nil
}

func (t *stubTicketing) MarkInProgress(_ context.Context, id string) error {
	t.markedProgress = append(t.markedProgress, id)
	return nil
}

func (t *stubTicketing) MarkComplete(_ context.Context, id string, _ engine.TaskResult) error {
	t.markedComplete = append(t.markedComplete, id)
	return nil
}

func (t *stubTicketing) MarkFailed(_ context.Context, id string, _ string) error {
	t.markedFailed = append(t.markedFailed, id)
	return nil
}

func (t *stubTicketing) AddComment(_ context.Context, _ string, _ string) error { return nil }
func (t *stubTicketing) Name() string                                           { return "stub" }
func (t *stubTicketing) InterfaceVersion() int                                  { return 1 }

type stubJobBuilder struct{}

func (b *stubJobBuilder) Build(taskRunID string, _ string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job-" + taskRunID,
			Namespace: "test-ns",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "agent", Image: spec.Image, Command: spec.Command},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}, nil
}
