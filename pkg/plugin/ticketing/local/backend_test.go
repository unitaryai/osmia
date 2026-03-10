package local

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newBackendForTest(t *testing.T) *Backend {
	t.Helper()
	backend, err := New(Config{StorePath: ":memory:"}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})
	return backend
}

func seedBackendForTest(t *testing.T) *Backend {
	t.Helper()
	backend := newBackendForTest(t)
	err := backend.CreateTicket(context.Background(), ticketing.Ticket{
		ID:          "LOCAL-1",
		Title:       "Fix login bug",
		Description: "Users cannot log in",
		TicketType:  "bug",
		Labels:      []string{"robodev", "urgent"},
		RepoURL:     "https://github.com/example/repo",
		ExternalURL: "https://local.test/tickets/LOCAL-1",
		Raw:         map[string]any{"priority": "high"},
	})
	require.NoError(t, err)
	return backend
}

func TestBackend_Name(t *testing.T) {
	backend := newBackendForTest(t)
	assert.Equal(t, backendName, backend.Name())
}

func TestBackend_InterfaceVersion(t *testing.T) {
	backend := newBackendForTest(t)
	assert.Equal(t, ticketing.InterfaceVersion, backend.InterfaceVersion())
}

func TestPollReadyTickets_ReturnsOrderedReadyTickets(t *testing.T) {
	backend := newBackendForTest(t)
	ctx := context.Background()

	require.NoError(t, backend.CreateTicket(ctx, ticketing.Ticket{ID: "LOCAL-2", Title: "Second"}))
	require.NoError(t, backend.CreateTicket(ctx, ticketing.Ticket{ID: "LOCAL-1", Title: "First"}))
	require.NoError(t, backend.MarkInProgress(ctx, "LOCAL-2"))

	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	require.Len(t, tickets, 1)
	assert.Equal(t, "LOCAL-1", tickets[0].ID)
	assert.Equal(t, []string(nil), tickets[0].Labels)
}

func TestMarkInProgress_PersistsState(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()

	require.NoError(t, backend.MarkInProgress(ctx, "LOCAL-1"))

	record, err := backend.GetTicket(ctx, "LOCAL-1")
	require.NoError(t, err)
	assert.Equal(t, StatusInProgress, record.Status)
	assert.Equal(t, RunStateRunning, record.RunState)
	require.NotNil(t, record.InProgressAt)

	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	assert.Empty(t, tickets)
}

func TestMarkComplete_PersistsResultAndSystemComment(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()
	require.NoError(t, backend.MarkInProgress(ctx, "LOCAL-1"))

	result := engine.TaskResult{
		Success:         true,
		Summary:         "Implemented the requested fix",
		BranchName:      "feature/local-1",
		MergeRequestURL: "https://example.test/mr/1",
		TokenUsage: &engine.TokenUsage{
			InputTokens:  123,
			OutputTokens: 456,
		},
		CostEstimateUSD: 0.42,
		ExitCode:        0,
	}
	require.NoError(t, backend.MarkComplete(ctx, "LOCAL-1", result))

	record, err := backend.GetTicket(ctx, "LOCAL-1")
	require.NoError(t, err)
	assert.Equal(t, StatusDone, record.Status)
	assert.Equal(t, RunStateSucceeded, record.RunState)
	require.NotNil(t, record.Result)
	assert.Equal(t, result.Summary, record.Result.Summary)
	assert.Equal(t, result.MergeRequestURL, record.Result.MergeRequestURL)

	comments, err := backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, CommentKindSystem, comments[0].Kind)
	assert.Contains(t, comments[0].Body, result.Summary)
	assert.Contains(t, comments[0].Body, result.MergeRequestURL)

	require.NoError(t, backend.MarkComplete(ctx, "LOCAL-1", result))
	comments, err = backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
}

func TestMarkFailed_PersistsFailureAndSystemComment(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()
	reason := "guard rail rejected the task"

	require.NoError(t, backend.MarkFailed(ctx, "LOCAL-1", reason))

	record, err := backend.GetTicket(ctx, "LOCAL-1")
	require.NoError(t, err)
	assert.Equal(t, StatusTodo, record.Status)
	assert.Equal(t, RunStateFailed, record.RunState)
	assert.Equal(t, reason, record.FailureReason)
	require.NotNil(t, record.FailedAt)

	tickets, err := backend.PollReadyTickets(ctx)
	require.NoError(t, err)
	assert.Empty(t, tickets)

	comments, err := backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, CommentKindSystem, comments[0].Kind)
	assert.Contains(t, comments[0].Body, reason)

	require.NoError(t, backend.MarkFailed(ctx, "LOCAL-1", reason))
	comments, err = backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
}

func TestMarkFailed_AfterStart_KeepsTrackerStatusInProgress(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()

	require.NoError(t, backend.MarkInProgress(ctx, "LOCAL-1"))
	require.NoError(t, backend.MarkFailed(ctx, "LOCAL-1", "worker crashed"))

	record, err := backend.GetTicket(ctx, "LOCAL-1")
	require.NoError(t, err)
	assert.Equal(t, StatusInProgress, record.Status)
	assert.Equal(t, RunStateFailed, record.RunState)
	assert.Equal(t, "worker crashed", record.FailureReason)
	require.NotNil(t, record.InProgressAt)
	require.NotNil(t, record.FailedAt)
}

func TestAddComment_PersistsSystemComment(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()

	require.NoError(t, backend.AddComment(ctx, "LOCAL-1", "progress update"))

	comments, err := backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, CommentKindSystem, comments[0].Kind)
	assert.Equal(t, "progress update", comments[0].Body)
}

func TestAddUserComment_PersistsUserComment(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()

	require.NoError(t, backend.AddUserComment(ctx, "LOCAL-1", "human note"))

	comments, err := backend.ListComments(ctx, "LOCAL-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, CommentKindUser, comments[0].Kind)
	assert.Equal(t, "human note", comments[0].Body)
}

func TestWriteMethods_UnknownTicketFail(t *testing.T) {
	backend := newBackendForTest(t)
	ctx := context.Background()

	err := backend.MarkInProgress(ctx, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)

	err = backend.MarkComplete(ctx, "missing", engine.TaskResult{Summary: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)

	err = backend.MarkFailed(ctx, "missing", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)

	err = backend.AddComment(ctx, "missing", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)
}

func TestSeedFile_ImportsMissingTicketsOnly(t *testing.T) {
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "tasks.yaml")
	content := `- id: "LOCAL-2"
  title: "Imported task"
  description: "From seed file"
  ticket_type: "feature"
  repo_url: "https://github.com/example/repo"
  labels:
    - zed
    - alpha
`
	require.NoError(t, os.WriteFile(seedFile, []byte(content), 0o644))

	backend, err := New(Config{
		StorePath: filepath.Join(dir, "local.db"),
		SeedFile:  seedFile,
	}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	tickets, err := backend.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 1)
	assert.Equal(t, "LOCAL-2", tickets[0].ID)
	assert.Equal(t, []string{"alpha", "zed"}, tickets[0].Labels)

	require.NoError(t, backend.MarkInProgress(context.Background(), "LOCAL-2"))

	backend2, err := New(Config{
		StorePath: filepath.Join(dir, "local.db"),
		SeedFile:  seedFile,
	}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend2.Close())
	})

	tickets, err = backend2.PollReadyTickets(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tickets)
}

func TestSeedFile_DuplicateIDsFail(t *testing.T) {
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "tasks.yaml")
	content := `- id: "LOCAL-1"
  title: "First"
- id: "LOCAL-1"
  title: "Duplicate"
`
	require.NoError(t, os.WriteFile(seedFile, []byte(content), 0o644))

	_, err := New(Config{
		StorePath: filepath.Join(dir, "local.db"),
		SeedFile:  seedFile,
	}, testLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate ticket id "LOCAL-1"`)
}

func TestSeedFile_MalformedYAMLFails(t *testing.T) {
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "tasks.yaml")
	require.NoError(t, os.WriteFile(seedFile, []byte("not: [valid"), 0o644))

	_, err := New(Config{
		StorePath: filepath.Join(dir, "local.db"),
		SeedFile:  seedFile,
	}, testLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing seed file")
}

func TestCreateTicket_DuplicateIDFails(t *testing.T) {
	backend := seedBackendForTest(t)
	err := backend.CreateTicket(context.Background(), ticketing.Ticket{ID: "LOCAL-1", Title: "Duplicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")
}

func TestRequeue_ClearsTerminalState(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()
	require.NoError(t, backend.MarkInProgress(ctx, "LOCAL-1"))
	require.NoError(t, backend.MarkFailed(ctx, "LOCAL-1", "test failure"))

	require.NoError(t, backend.RequeueTicket(ctx, "LOCAL-1"))

	record, err := backend.GetTicket(ctx, "LOCAL-1")
	require.NoError(t, err)
	assert.Equal(t, StatusTodo, record.Status)
	assert.Equal(t, RunStateIdle, record.RunState)
	assert.Equal(t, "", record.FailureReason)
	assert.Nil(t, record.Result)
	assert.Nil(t, record.InProgressAt)
	assert.Nil(t, record.FailedAt)
}

func TestStorePath_CreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "nested", "tickets.db")

	backend, err := New(Config{StorePath: storePath}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	_, err = os.Stat(storePath)
	require.NoError(t, err)
}

func TestEvents_ArePersistedAsJSON(t *testing.T) {
	backend := seedBackendForTest(t)
	ctx := context.Background()
	require.NoError(t, backend.MarkFailed(ctx, "LOCAL-1", "boom"))

	rows, err := backend.db.QueryContext(ctx, `SELECT event_type, payload_json FROM ticket_events WHERE ticket_id = ? ORDER BY id ASC`, "LOCAL-1")
	require.NoError(t, err)
	defer rows.Close()

	var (
		eventTypes []string
		reason     string
	)
	for rows.Next() {
		var eventType string
		var payloadJSON string
		require.NoError(t, rows.Scan(&eventType, &payloadJSON))
		eventTypes = append(eventTypes, eventType)
		if eventType == "marked_failed" {
			var payload map[string]string
			require.NoError(t, json.Unmarshal([]byte(payloadJSON), &payload))
			reason = payload["reason"]
		}
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"created", "marked_failed"}, eventTypes)
	assert.Equal(t, "boom", reason)
}

func TestListComments_UnknownTicketFails(t *testing.T) {
	backend := newBackendForTest(t)

	comments, err := backend.ListComments(context.Background(), "missing")
	require.Error(t, err)
	assert.Nil(t, comments)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)
}

func TestScanTicketRecord_EmptyResultJSONYieldsNilResult(t *testing.T) {
	backend := seedBackendForTest(t)

	rows, err := backend.db.Query(`SELECT id, title, description, ticket_type, labels_json, repo_url, external_url, raw_json, state, run_state, result_json, failure_reason, created_at, updated_at, in_progress_at, completed_at, failed_at FROM tickets WHERE id = ?`, "LOCAL-1")
	require.NoError(t, err)
	defer rows.Close()

	records, err := scanStoredTickets(rows)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Nil(t, records[0].Result)
	assert.Equal(t, StatusTodo, records[0].Status)
	assert.Equal(t, RunStateIdle, records[0].RunState)
}

func TestCurrentLifecycle_UnknownTicketError(t *testing.T) {
	backend := newBackendForTest(t)
	err := backend.runInTx(context.Background(), func(ctx context.Context, tx txRunner) error {
		_, _, err := loadTicketLifecycle(ctx, tx, "missing")
		return err
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ticket "missing" not found`)
}
