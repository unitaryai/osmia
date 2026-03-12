//go:build integration

// Package integration_test contains integration tests for local development
// mode features: DockerBuilder job generation, the local SQLite ticketing
// backend, and builder selection logic.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/sandboxbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	localticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/local"
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
	assert.Equal(t, "local", job.ObjectMeta.Annotations["osmia.io/execution-backend"],
		"Job must be annotated with local execution backend")

	// Verify local backend annotation on pod template.
	assert.Equal(t, "local", job.Spec.Template.ObjectMeta.Annotations["osmia.io/execution-backend"],
		"pod template must be annotated with local execution backend")

	// Verify standard labels are present.
	assert.Equal(t, "osmia-agent", job.Labels["app"])
	assert.Equal(t, "claude-code", job.Labels["osmia.io/engine"])
	assert.Equal(t, "tr-local-1", job.Labels["osmia.io/task-run-id"])

	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, spec.Image, job.Spec.Template.Spec.Containers[0].Image)

	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, sc)
	assert.True(t, *sc.RunAsNonRoot)
	assert.True(t, *sc.ReadOnlyRootFilesystem)
	assert.False(t, *sc.AllowPrivilegeEscalation)
}

// TestLocalBackendImportsSeedFile verifies that the local backend imports
// ready tickets from a one-time seed file into SQLite.
func TestLocalBackendImportsSeedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	seedFile := filepath.Join(dir, "tasks.yaml")
	storePath := filepath.Join(dir, "local-ticketing.db")
	content := `- id: "LOCAL-1"
  title: "First local task"
  description: "Description for first task"
  repo_url: "https://github.com/org/repo"
  labels:
    - osmia
- id: "LOCAL-2"
  title: "Second local task"
  repo_url: "https://github.com/org/repo2"
`
	err := os.WriteFile(seedFile, []byte(content), 0o644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	backend, err := localticket.New(localticket.Config{
		StorePath: storePath,
		SeedFile:  seedFile,
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	tickets, err := backend.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 2)
	assert.Equal(t, "LOCAL-1", tickets[0].ID)
	assert.Equal(t, "First local task", tickets[0].Title)
	assert.Equal(t, "Description for first task", tickets[0].Description)
	assert.Equal(t, "https://github.com/org/repo", tickets[0].RepoURL)
	assert.Equal(t, "LOCAL-2", tickets[1].ID)
}

// TestLocalBackendPersistsTicketState verifies that after moving a ticket to
// in-progress, re-opening the backend does not re-expose it as ready work.
func TestLocalBackendPersistsTicketState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	seedFile := filepath.Join(dir, "tasks.yaml")
	storePath := filepath.Join(dir, "local-ticketing.db")
	content := `- id: "EXCL-1"
  title: "Task one"
- id: "EXCL-2"
  title: "Task two"
`
	err := os.WriteFile(seedFile, []byte(content), 0o644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	backend, err := localticket.New(localticket.Config{
		StorePath: storePath,
		SeedFile:  seedFile,
	}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	require.Len(t, tickets, 2)
	require.NoError(t, backend.MarkInProgress(ctx, "EXCL-1"))
	require.NoError(t, backend.Close())

	backend, err = localticket.New(localticket.Config{
		StorePath: storePath,
		SeedFile:  seedFile,
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	tickets, err = backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	require.Len(t, tickets, 1)
	assert.Equal(t, "EXCL-2", tickets[0].ID)
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
			name:    "job_backend_selects_standard_builder",
			backend: "job",
		},
		{
			name:    "empty_backend_selects_standard_builder",
			backend: "",
		},
	}

	spec := localDevTestSpec()

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			switch tc.backend {
			case "local":
				db := jobbuilder.NewDockerBuilder("test-ns")
				job, err := db.Build("tr-sel-1", "claude-code", spec)
				require.NoError(t, err)
				assert.Equal(t, "local", job.ObjectMeta.Annotations["osmia.io/execution-backend"],
					"local backend must produce docker-annotated jobs")

			case "sandbox":
				sb := newSandboxBuilderForTest("test-ns")
				job, err := sb.Build("tr-sel-2", "claude-code", spec)
				require.NoError(t, err)
				require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
				assert.Equal(t, "gvisor", *job.Spec.Template.Spec.RuntimeClassName)

			default:
				jb := jobbuilder.NewJobBuilder("test-ns")
				job, err := jb.Build("tr-sel-3", "claude-code", spec)
				require.NoError(t, err)
				_, hasAnnotation := job.ObjectMeta.Annotations["osmia.io/execution-backend"]
				assert.False(t, hasAnnotation,
					"standard job backend must not have execution-backend annotation")
				assert.Nil(t, job.Spec.Template.Spec.RuntimeClassName,
					"standard job backend must not set RuntimeClassName")
			}
		})
	}
}
