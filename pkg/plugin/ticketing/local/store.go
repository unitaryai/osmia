package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

const ticketSelectColumns = `id, title, description, ticket_type, labels_json, repo_url, external_url, raw_json,
	state, run_state, result_json, failure_reason, created_at, updated_at, in_progress_at, completed_at, failed_at`

type rowScanner interface {
	Scan(dest ...any) error
}

type txRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ListTickets returns all persisted local tickets ordered by creation time.
func (b *Backend) ListTickets(ctx context.Context) ([]StoredTicket, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT `+ticketSelectColumns+` FROM tickets ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing tickets: %w", err)
	}
	defer rows.Close()

	return scanStoredTickets(rows)
}

// GetTicket returns a single persisted local ticket.
func (b *Backend) GetTicket(ctx context.Context, id string) (*StoredTicket, error) {
	row := b.db.QueryRowContext(ctx,
		`SELECT `+ticketSelectColumns+` FROM tickets WHERE id = ?`,
		id,
	)

	ticket, err := scanStoredTicket(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("ticket %q not found", id)
		}
		return nil, err
	}

	return &ticket, nil
}

// CreateTicket inserts a new to-do ticket for local development workflows.
func (b *Backend) CreateTicket(ctx context.Context, ticket ticketing.Ticket) error {
	return b.runInTx(ctx, func(txContext context.Context, tx txRunner) error {
		return insertTicket(txContext, tx, ticket, eventCreated)
	})
}

// RequeueTicket moves a ticket back to to do so it can be picked up again.
func (b *Backend) RequeueTicket(ctx context.Context, id string) error {
	return b.runInTx(ctx, func(txContext context.Context, tx txRunner) error {
		status, runState, err := loadTicketLifecycle(txContext, tx, id)
		if err != nil {
			return err
		}
		if status == statusTodo && runState == runStateIdle {
			return nil
		}
		if runState == runStateRunning {
			return fmt.Errorf("ticket %q is currently running and cannot be moved to to do", id)
		}

		now := nowRFC3339()
		if err := setTicketTodo(txContext, tx, id, now); err != nil {
			return err
		}

		return insertEvent(txContext, tx, id, eventRequeued, map[string]string{"status": string(statusTodo)})
	})
}

// ListComments returns all persisted comments for a ticket in creation order.
func (b *Backend) ListComments(ctx context.Context, id string) ([]StoredComment, error) {
	if err := ensureTicketExists(ctx, b.db, id); err != nil {
		return nil, err
	}

	rows, err := b.db.QueryContext(ctx,
		`SELECT id, ticket_id, kind, body, created_at
		 FROM ticket_comments
		 WHERE ticket_id = ?
		 ORDER BY created_at ASC, id ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("listing comments for ticket %q: %w", id, err)
	}
	defer rows.Close()

	return scanComments(rows)
}

func (b *Backend) listReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, title, description, ticket_type, labels_json, repo_url, external_url, raw_json
		 FROM tickets
		 WHERE state = ? AND run_state = ?
		 ORDER BY created_at ASC, id ASC`,
		statusTodo,
		runStateIdle,
	)
	if err != nil {
		return nil, fmt.Errorf("listing ready tickets: %w", err)
	}
	defer rows.Close()

	return scanReadyTickets(rows)
}

func (b *Backend) addComment(ctx context.Context, kind CommentKind, ticketID, comment string) error {
	return b.runInTx(ctx, func(txContext context.Context, tx txRunner) error {
		if err := ensureTicketExists(txContext, tx, ticketID); err != nil {
			return err
		}
		if err := insertComment(txContext, tx, ticketID, kind, comment); err != nil {
			return err
		}

		payload := map[string]string{"body": comment, "kind": string(kind)}
		return insertEvent(txContext, tx, ticketID, eventCommentAdded, payload)
	})
}

func (b *Backend) transitionTicket(
	ctx context.Context,
	ticketID string,
	eventType EventType,
	result *engine.TaskResult,
	reason string,
) error {
	return b.runInTx(ctx, func(txContext context.Context, tx txRunner) error {
		status, runState, err := loadTicketLifecycle(txContext, tx, ticketID)
		if err != nil {
			return err
		}

		now := nowRFC3339()
		switch eventType {
		case eventMarkedInProgress:
			if status == statusInProgress && runState == runStateRunning {
				return nil
			}
			if err := setTicketInProgress(txContext, tx, ticketID, now); err != nil {
				return err
			}
		case eventMarkedComplete:
			if status == statusDone && runState == runStateSucceeded {
				return nil
			}
			if err := setTicketComplete(txContext, tx, ticketID, now, result); err != nil {
				return err
			}
			if err := insertComment(txContext, tx, ticketID, commentKindSystem, completionComment(*result)); err != nil {
				return err
			}
		case eventMarkedFailed:
			if runState == runStateFailed && reason != "" {
				return nil
			}
			if err := setTicketFailed(txContext, tx, ticketID, status, now, reason); err != nil {
				return err
			}
			if err := insertComment(txContext, tx, ticketID, commentKindSystem, failureComment(reason)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported transition event %q", eventType)
		}

		return insertTransitionEvent(txContext, tx, ticketID, eventType, status, runState, result, reason)
	})
}

func insertTransitionEvent(
	ctx context.Context,
	tx txRunner,
	ticketID string,
	eventType EventType,
	status Status,
	runState RunState,
	result *engine.TaskResult,
	reason string,
) error {
	payload := map[string]string{
		"previous_run_state": string(runState),
		"previous_status":    string(status),
	}
	if result != nil {
		payload["summary"] = result.Summary
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return insertEvent(ctx, tx, ticketID, eventType, payload)
}

func (b *Backend) runInTx(ctx context.Context, fn func(context.Context, txRunner) error) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

func insertTicket(ctx context.Context, runner txRunner, ticket ticketing.Ticket, eventType EventType) error {
	values, err := ticketInsertValues(ticket)
	if err != nil {
		return err
	}

	if _, err := runner.ExecContext(ctx, ticketInsertSQL(), values...); err != nil {
		return fmt.Errorf("creating ticket %q: %w", ticket.ID, err)
	}

	return insertEvent(ctx, runner, ticket.ID, eventType, ticket)
}

func insertTicketIfMissing(ctx context.Context, runner txRunner, ticket ticketing.Ticket, eventType EventType) error {
	values, err := ticketInsertValues(ticket)
	if err != nil {
		return err
	}

	result, err := runner.ExecContext(ctx, ticketInsertSQL()+` ON CONFLICT(id) DO NOTHING`, values...)
	if err != nil {
		return fmt.Errorf("importing ticket %q: %w", ticket.ID, err)
	}

	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking insert result for ticket %q: %w", ticket.ID, err)
	}
	if inserted == 0 {
		return nil
	}

	return insertEvent(ctx, runner, ticket.ID, eventType, ticket)
}

func ticketInsertSQL() string {
	return `INSERT INTO tickets (
		id, title, description, ticket_type, labels_json, repo_url, external_url, raw_json,
		state, run_state, result_json, summary, branch_name, merge_request_url, input_tokens,
		output_tokens, cost_estimate_usd, failure_reason, created_at, updated_at, in_progress_at,
		completed_at, failed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
}

func ticketInsertValues(ticket ticketing.Ticket) ([]any, error) {
	if ticket.ID == "" {
		return nil, fmt.Errorf("ticket id must not be empty")
	}
	if ticket.Title == "" {
		return nil, fmt.Errorf("ticket %q title must not be empty", ticket.ID)
	}

	labels := slices.Clone(ticket.Labels)
	slices.Sort(labels)

	labelsJSON, err := marshalJSON(labels, "[]")
	if err != nil {
		return nil, err
	}
	rawJSON, err := marshalJSON(ticket.Raw, "{}")
	if err != nil {
		return nil, err
	}

	now := nowRFC3339()
	return []any{
		ticket.ID,
		ticket.Title,
		ticket.Description,
		ticket.TicketType,
		labelsJSON,
		ticket.RepoURL,
		ticket.ExternalURL,
		rawJSON,
		statusTodo,
		runStateIdle,
		"",
		"",
		"",
		"",
		0,
		0,
		0.0,
		"",
		now,
		now,
		"",
		"",
		"",
	}, nil
}

func loadTicketLifecycle(ctx context.Context, runner txRunner, ticketID string) (Status, RunState, error) {
	var (
		status   Status
		runState RunState
	)
	err := runner.QueryRowContext(ctx, `SELECT state, run_state FROM tickets WHERE id = ?`, ticketID).Scan(&status, &runState)
	if err == nil {
		return status, runState, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("ticket %q not found", ticketID)
	}

	return "", "", fmt.Errorf("loading ticket %q lifecycle: %w", ticketID, err)
}

func ensureTicketExists(ctx context.Context, runner txRunner, ticketID string) error {
	_, _, err := loadTicketLifecycle(ctx, runner, ticketID)
	return err
}

func setTicketInProgress(ctx context.Context, runner txRunner, ticketID, now string) error {
	_, err := runner.ExecContext(ctx,
		`UPDATE tickets
		 SET state = ?, run_state = ?, result_json = '', summary = '', branch_name = '',
		     merge_request_url = '', input_tokens = 0, output_tokens = 0,
		     cost_estimate_usd = 0, failure_reason = '', updated_at = ?, in_progress_at = ?,
		     completed_at = '', failed_at = ''
		 WHERE id = ?`,
		statusInProgress, runStateRunning, now, now, ticketID,
	)
	if err != nil {
		return fmt.Errorf("marking ticket %q in progress: %w", ticketID, err)
	}

	return nil
}

func setTicketComplete(ctx context.Context, runner txRunner, ticketID, now string, result *engine.TaskResult) error {
	resultJSON, err := marshalJSON(result, "")
	if err != nil {
		return err
	}
	tokensIn, tokensOut := tokenCounts(result)
	_, err = runner.ExecContext(ctx,
		`UPDATE tickets
		 SET state = ?, run_state = ?, result_json = ?, summary = ?, branch_name = ?,
		     merge_request_url = ?, input_tokens = ?, output_tokens = ?,
		     cost_estimate_usd = ?, failure_reason = '', updated_at = ?, completed_at = ?,
		     failed_at = ''
		 WHERE id = ?`,
		statusDone, runStateSucceeded, resultJSON, result.Summary, result.BranchName,
		result.MergeRequestURL, tokensIn, tokensOut, result.CostEstimateUSD, now, now, ticketID,
	)
	if err != nil {
		return fmt.Errorf("marking ticket %q complete: %w", ticketID, err)
	}

	return nil
}

func setTicketFailed(ctx context.Context, runner txRunner, ticketID string, status Status, now, reason string) error {
	_, err := runner.ExecContext(ctx,
		`UPDATE tickets
		 SET state = ?, run_state = ?, failure_reason = ?, updated_at = ?, failed_at = ?,
		     completed_at = ''
		 WHERE id = ?`,
		status, runStateFailed, reason, now, now, ticketID,
	)
	if err != nil {
		return fmt.Errorf("marking ticket %q failed: %w", ticketID, err)
	}

	return nil
}

func setTicketTodo(ctx context.Context, runner txRunner, ticketID, now string) error {
	_, err := runner.ExecContext(ctx,
		`UPDATE tickets
		 SET state = ?, run_state = ?, result_json = '', summary = '', branch_name = '',
		     merge_request_url = '', input_tokens = 0, output_tokens = 0,
		     cost_estimate_usd = 0, failure_reason = '', updated_at = ?, in_progress_at = '',
		     completed_at = '', failed_at = ''
		 WHERE id = ?`,
		statusTodo, runStateIdle, now, ticketID,
	)
	if err != nil {
		return fmt.Errorf("moving ticket %q to to do: %w", ticketID, err)
	}

	return nil
}

func insertComment(ctx context.Context, runner txRunner, ticketID string, kind CommentKind, body string) error {
	_, err := runner.ExecContext(ctx,
		`INSERT INTO ticket_comments (ticket_id, kind, body, created_at) VALUES (?, ?, ?, ?)`,
		ticketID, kind, body, nowRFC3339(),
	)
	if err != nil {
		return fmt.Errorf("adding comment to ticket %q: %w", ticketID, err)
	}

	return nil
}

func insertEvent(ctx context.Context, runner txRunner, ticketID string, eventType EventType, payload any) error {
	payloadJSON, err := marshalJSON(payload, "{}")
	if err != nil {
		return err
	}

	_, err = runner.ExecContext(ctx,
		`INSERT INTO ticket_events (ticket_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?)`,
		ticketID, eventType, payloadJSON, nowRFC3339(),
	)
	if err != nil {
		return fmt.Errorf("adding event %q to ticket %q: %w", eventType, ticketID, err)
	}

	return nil
}

func scanReadyTickets(rows *sql.Rows) ([]ticketing.Ticket, error) {
	var tickets []ticketing.Ticket
	for rows.Next() {
		ticket, err := scanReadyTicket(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ready tickets: %w", err)
	}

	return tickets, nil
}

func scanReadyTicket(scanner rowScanner) (ticketing.Ticket, error) {
	var (
		ticket     ticketing.Ticket
		labelsJSON string
		rawJSON    string
	)
	if err := scanner.Scan(
		&ticket.ID,
		&ticket.Title,
		&ticket.Description,
		&ticket.TicketType,
		&labelsJSON,
		&ticket.RepoURL,
		&ticket.ExternalURL,
		&rawJSON,
	); err != nil {
		return ticketing.Ticket{}, fmt.Errorf("scanning ready ticket: %w", err)
	}
	if err := decodeJSON(labelsJSON, &ticket.Labels); err != nil {
		return ticketing.Ticket{}, err
	}
	if err := decodeJSON(rawJSON, &ticket.Raw); err != nil {
		return ticketing.Ticket{}, err
	}

	return ticket, nil
}

func scanStoredTickets(rows *sql.Rows) ([]StoredTicket, error) {
	var tickets []StoredTicket
	for rows.Next() {
		ticket, err := scanStoredTicket(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stored tickets: %w", err)
	}

	return tickets, nil
}

func scanStoredTicket(scanner rowScanner) (StoredTicket, error) {
	var (
		labelsJSON   string
		rawJSON      string
		resultJSON   string
		createdAt    string
		updatedAt    string
		inProgressAt string
		completedAt  string
		failedAt     string
		ticket       StoredTicket
	)

	if err := scanner.Scan(
		&ticket.Ticket.ID,
		&ticket.Ticket.Title,
		&ticket.Ticket.Description,
		&ticket.Ticket.TicketType,
		&labelsJSON,
		&ticket.Ticket.RepoURL,
		&ticket.Ticket.ExternalURL,
		&rawJSON,
		&ticket.Status,
		&ticket.RunState,
		&resultJSON,
		&ticket.FailureReason,
		&createdAt,
		&updatedAt,
		&inProgressAt,
		&completedAt,
		&failedAt,
	); err != nil {
		return StoredTicket{}, fmt.Errorf("scanning stored ticket: %w", err)
	}

	if err := decodeStoredTicketJSON(&ticket, labelsJSON, rawJSON, resultJSON); err != nil {
		return StoredTicket{}, err
	}
	if err := decodeStoredTicketTimes(&ticket, createdAt, updatedAt, inProgressAt, completedAt, failedAt); err != nil {
		return StoredTicket{}, err
	}

	return ticket, nil
}

func decodeStoredTicketJSON(ticket *StoredTicket, labelsJSON, rawJSON, resultJSON string) error {
	if err := decodeJSON(labelsJSON, &ticket.Ticket.Labels); err != nil {
		return err
	}
	if err := decodeJSON(rawJSON, &ticket.Ticket.Raw); err != nil {
		return err
	}
	if resultJSON == "" {
		return nil
	}

	var result engine.TaskResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return fmt.Errorf("unmarshalling task result: %w", err)
	}
	ticket.Result = &result
	return nil
}

func decodeStoredTicketTimes(
	ticket *StoredTicket,
	createdAt string,
	updatedAt string,
	inProgressAt string,
	completedAt string,
	failedAt string,
) error {
	var err error
	if ticket.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return fmt.Errorf("parsing created_at: %w", err)
	}
	if ticket.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return fmt.Errorf("parsing updated_at: %w", err)
	}
	if ticket.InProgressAt, err = parseOptionalTime(inProgressAt); err != nil {
		return err
	}
	if ticket.CompletedAt, err = parseOptionalTime(completedAt); err != nil {
		return err
	}
	if ticket.FailedAt, err = parseOptionalTime(failedAt); err != nil {
		return err
	}
	return nil
}

func scanComments(rows *sql.Rows) ([]StoredComment, error) {
	var comments []StoredComment
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating comments: %w", err)
	}

	return comments, nil
}

func scanComment(scanner rowScanner) (StoredComment, error) {
	var (
		comment   StoredComment
		createdAt string
	)
	if err := scanner.Scan(&comment.ID, &comment.TicketID, &comment.Kind, &comment.Body, &createdAt); err != nil {
		return StoredComment{}, fmt.Errorf("scanning comment: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return StoredComment{}, fmt.Errorf("parsing comment created_at: %w", err)
	}
	comment.CreatedAt = parsed
	return comment, nil
}

func decodeJSON[T any](raw string, target *T) error {
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("unmarshalling json payload: %w", err)
	}
	return nil
}

func marshalJSON(value any, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshalling json payload: %w", err)
	}
	return string(encoded), nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("parsing optional time: %w", err)
	}

	return &parsed, nil
}

func tokenCounts(result *engine.TaskResult) (int, int) {
	if result == nil || result.TokenUsage == nil {
		return 0, 0
	}

	return result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func completionComment(result engine.TaskResult) string {
	comment := fmt.Sprintf("Run completed successfully.\n\n**Summary:** %s", result.Summary)
	if result.MergeRequestURL != "" {
		comment += fmt.Sprintf("\n**Merge Request:** %s", result.MergeRequestURL)
	}
	return comment
}

func failureComment(reason string) string {
	return fmt.Sprintf("Run failed.\n\n**Reason:** %s", reason)
}
