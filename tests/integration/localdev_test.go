//go:build integration

// Package integration_test contains integration tests for local development
// mode features: DockerBuilder job generation, noop file-watcher ticketing,
// and builder selection logic.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/jobbuilder"
	"github.com/unitaryai/robodev/internal/sandboxbuilder"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/engine/claudecode"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing/noop"
)

// newSandboxBuilderForTest creates a SandboxBuilder with default config for tests.
func newSandboxBuilderForTest(namespace string) *sandboxbuilder.SandboxBuilder {
	return sandboxbuilder.New(namespace, config.SandboxConfig{})
}

// localDevTestSpec returns a standard ExecutionSpec for local dev tests.
func localDevTestSpec() *engine.ExecutionSpec {
	eng := claudecode.New()
	spec, _ := eng.BuildExecutionSpec(engine.Task{
		ID:       "local-test-1",
		TicketID: "TICKET-LOCAL-1",
		Title:    "Local dev test task",
		RepoURL:  "https://github.com/org/repo",
	}, engine.EngineConfig{})
	return spec
}

// TestDockerBuilderProducesValidJob verifies that the DockerBuilder produces
// a Job annotated with the local execution backend.
func TestDockerBuilderProducesValidJob(t *testing.T) {
	t.Parallel()

	db := jobbuilder.NewDockerBuilder("test-ns")
	spec := localDevTestSpec()

	job, err := db.Build("tr-local-1", "claude-code", spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	// Verify local backend annotation on Job metadata.
	assert.Equal(t, "local", job.ObjectMeta.Annotations["robodev.io/execution-backend"],
		"Job must be annotated with local execution backend")

	// Verify local backend annotation on pod template.
	assert.Equal(t, "local", job.Spec.Template.ObjectMeta.Annotations["robodev.io/execution-backend"],
		"pod template must be annotated with local execution backend")

	// Verify standard labels are present.
	assert.Equal(t, "robodev-agent", job.Labels["app"])
	assert.Equal(t, "claude-code", job.Labels["robodev.io/engine"])
	assert.Equal(t, "tr-local-1", job.Labels["robodev.io/task-run-id"])

	// Verify container exists with correct image.
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, spec.Image, job.Spec.Template.Spec.Containers[0].Image)

	// Verify security context is present and restrictive.
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, sc)
	assert.True(t, *sc.RunAsNonRoot)
	assert.True(t, *sc.ReadOnlyRootFilesystem)
	assert.False(t, *sc.AllowPrivilegeEscalation)
}

// TestNoopFileWatcherReadsTasks verifies that the noop backend configured
// with a task file reads tickets from the YAML file.
func TestNoopFileWatcherReadsTasks(t *testing.T) {
	t.Parallel()

	// Create a temporary YAML task file.
	dir := t.TempDir()
	taskFile := filepath.Join(dir, "tasks.yaml")
	content := `- id: "LOCAL-1"
  title: "First local task"
  description: "Description for first task"
  repo_url: "https://github.com/org/repo"
  labels:
    - robodev
- id: "LOCAL-2"
  title: "Second local task"
  repo_url: "https://github.com/org/repo2"
`
	err := os.WriteFile(taskFile, []byte(content), 0644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	backend := noop.NewWithTaskFile(logger, taskFile)

	ctx := context.Background()
	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	require.Len(t, tickets, 2, "should read 2 tickets from file")

	assert.Equal(t, "LOCAL-1", tickets[0].ID)
	assert.Equal(t, "First local task", tickets[0].Title)
	assert.Equal(t, "Description for first task", tickets[0].Description)
	assert.Equal(t, "https://github.com/org/repo", tickets[0].RepoURL)

	assert.Equal(t, "LOCAL-2", tickets[1].ID)
	assert.Equal(t, "Second local task", tickets[1].Title)
}

// TestNoopFileWatcherExcludesProcessed verifies that after marking a ticket
// in-progress, subsequent polls exclude that ticket.
func TestNoopFileWatcherExcludesProcessed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	taskFile := filepath.Join(dir, "tasks.yaml")
	content := `- id: "EXCL-1"
  title: "Task one"
- id: "EXCL-2"
  title: "Task two"
`
	err := os.WriteFile(taskFile, []byte(content), 0644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	backend := noop.NewWithTaskFile(logger, taskFile)

	ctx := context.Background()

	// First poll: both tickets returned.
	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	require.Len(t, tickets, 2)

	// Mark both in-progress.
	for _, ticket := range tickets {
		err = backend.MarkInProgress(ctx, ticket.ID)
		require.NoError(t, err)
	}

	// Second poll: no tickets returned (all processed).
	tickets, err = backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	assert.Empty(t, tickets, "second poll should return empty after all tickets processed")
}

// TestNoopBackendWithoutFileReturnsEmpty verifies that the standard noop
// backend (no task file) returns an empty ticket list.
func TestNoopBackendWithoutFileReturnsEmpty(t *testing.T) {
	t.Parallel()

	backend := noop.New()
	ctx := context.Background()

	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	assert.Empty(t, tickets, "noop backend without task file should return empty")
}

// TestBuilderSelectionByBackend verifies that the execution backend
// configuration drives the correct builder choice. This tests the logic
// conceptually rather than main.go wiring.
func TestBuilderSelectionByBackend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		backend     string
		wantLocal   bool
		wantSandbox bool
	}{
		{
			name:      "local_backend_selects_docker_builder",
			backend:   "local",
			wantLocal: true,
		},
		{
			name:        "sandbox_backend_selects_sandbox_builder",
			backend:     "sandbox",
			wantSandbox: true,
		},
		{
			name: "job_backend_selects_standard_builder",
			backend: "job",
		},
		{
			name: "empty_backend_selects_standard_builder",
			backend: "",
		},
	}

	spec := localDevTestSpec()

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Select the builder based on the backend string, mirroring
			// the selection logic that main.go would use.
			var builder interface {
				Build(string, string, *engine.ExecutionSpec) (*interface{}, error)
			}
			_ = builder // we don't actually call it; we test the selection logic

			switch tc.backend {
			case "local":
				db := jobbuilder.NewDockerBuilder("test-ns")
				job, err := db.Build("tr-sel-1", "claude-code", spec)
				require.NoError(t, err)
				assert.Equal(t, "local", job.ObjectMeta.Annotations["robodev.io/execution-backend"],
					"local backend must produce docker-annotated jobs")

			case "sandbox":
				// Sandbox builder should produce jobs with RuntimeClassName.
				// Importing sandboxbuilder here to verify the selection.
				sb := newSandboxBuilderForTest("test-ns")
				job, err := sb.Build("tr-sel-2", "claude-code", spec)
				require.NoError(t, err)
				require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
				assert.Equal(t, "gvisor", *job.Spec.Template.Spec.RuntimeClassName,
					"sandbox backend must produce RuntimeClassName-annotated jobs")

			default:
				// Standard builder: no backend annotation, no RuntimeClassName.
				jb := jobbuilder.NewJobBuilder("test-ns")
				job, err := jb.Build("tr-sel-3", "claude-code", spec)
				require.NoError(t, err)
				_, hasAnnotation := job.ObjectMeta.Annotations["robodev.io/execution-backend"]
				assert.False(t, hasAnnotation,
					"standard job backend must not have execution-backend annotation")
				assert.Nil(t, job.Spec.Template.Spec.RuntimeClassName,
					"standard job backend must not set RuntimeClassName")
			}
		})
	}
}
