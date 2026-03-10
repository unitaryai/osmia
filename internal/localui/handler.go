package localui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
	localticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/local"
)

// maxRequestBodyBytes caps the size of JSON request bodies to prevent
// unbounded memory consumption from oversized payloads.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

//go:embed index.html
var assets embed.FS

type service interface {
	ListTickets(ctx context.Context) ([]localticket.StoredTicket, error)
	GetTicket(ctx context.Context, id string) (*localticket.StoredTicket, error)
	ListComments(ctx context.Context, id string) ([]localticket.StoredComment, error)
	CreateTicket(ctx context.Context, ticket ticketing.Ticket) error
	RequeueTicket(ctx context.Context, id string) error
	AddUserComment(ctx context.Context, id string, comment string) error
}

type trackerStatus string

const (
	trackerStatusDone       trackerStatus = "done"
	trackerStatusInProgress trackerStatus = "in_progress"
	trackerStatusTodo       trackerStatus = "todo"
)

type runOutcome string

const (
	runOutcomeFailed    runOutcome = "failed"
	runOutcomeIdle      runOutcome = "idle"
	runOutcomeRunning   runOutcome = "running"
	runOutcomeSucceeded runOutcome = "succeeded"
)

type ticketView struct {
	Ticket          ticketing.Ticket `json:"ticket"`
	TrackerStatus   trackerStatus    `json:"tracker_status"`
	RunAgainAllowed bool             `json:"run_again_allowed"`
	RunOutcome      runOutcome       `json:"run_outcome"`
	RunSummary      string           `json:"run_summary"`
	NeedsAttention  bool             `json:"needs_attention"`
	FailureReason   string           `json:"failure_reason"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	InProgressAt    *time.Time       `json:"in_progress_at,omitempty"`
	CompletedAt     *time.Time       `json:"completed_at,omitempty"`
	FailedAt        *time.Time       `json:"failed_at,omitempty"`
}

type commentView struct {
	ID        int64     `json:"id"`
	TicketID  string    `json:"ticket_id"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Handler serves the local ticketing UI and its JSON API.
type Handler struct {
	logger  *slog.Logger
	service service
	page    []byte
}

// NewHandler returns an HTTP handler for the local ticketing UI.
func NewHandler(logger *slog.Logger, svc service) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		return nil, fmt.Errorf("reading embedded UI: %w", err)
	}

	h := &Handler{
		logger:  logger,
		service: svc,
		page:    page,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.handleIndex)
	mux.HandleFunc("GET /api/tickets", h.handleListTickets)
	mux.HandleFunc("GET /api/tickets/{id}", h.handleGetTicket)
	mux.HandleFunc("GET /api/tickets/{id}/comments", h.handleListComments)
	mux.HandleFunc("POST /api/tickets", h.handleCreateTicket)
	mux.HandleFunc("POST /api/tickets/{id}/comments", h.handleAddComment)
	mux.HandleFunc("POST /api/tickets/{id}/requeue", h.handleRequeueTicket)
	return mux, nil
}

func (h *Handler) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.page)
}

func (h *Handler) handleListTickets(w http.ResponseWriter, r *http.Request) {
	tickets, err := h.service.ListTickets(r.Context())
	if err != nil {
		h.writeError(w, err, http.StatusInternalServerError)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"tickets": ensureTicketViews(tickets)})
}

func (h *Handler) handleGetTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	ticket, err := h.service.GetTicket(r.Context(), ticketID)
	if err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"ticket": newTicketView(*ticket)})
}

func (h *Handler) handleListComments(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	comments, err := h.service.ListComments(r.Context(), ticketID)
	if err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"comments": ensureCommentViews(comments)})
}

func (h *Handler) handleCreateTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		TicketType  string   `json:"ticket_type"`
		Labels      []string `json:"labels"`
		RepoURL     string   `json:"repo_url"`
		ExternalURL string   `json:"external_url"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := decodeJSONBody(r.Body, &req); err != nil {
		h.writeError(w, err, http.StatusBadRequest)
		return
	}

	ticket := ticketing.Ticket{
		ID:          strings.TrimSpace(req.ID),
		Title:       strings.TrimSpace(req.Title),
		Description: strings.TrimSpace(req.Description),
		TicketType:  strings.TrimSpace(req.TicketType),
		Labels:      trimSlice(req.Labels),
		RepoURL:     strings.TrimSpace(req.RepoURL),
		ExternalURL: strings.TrimSpace(req.ExternalURL),
	}
	if ticket.ID == "" {
		h.writeError(w, fmt.Errorf("ticket id must not be empty"), http.StatusBadRequest)
		return
	}
	if ticket.Title == "" {
		h.writeError(w, fmt.Errorf("ticket title must not be empty"), http.StatusBadRequest)
		return
	}

	err := h.service.CreateTicket(r.Context(), ticket)
	if err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}

	storedTicket, err := h.service.GetTicket(r.Context(), ticket.ID)
	if err != nil {
		h.writeError(w, err, http.StatusInternalServerError)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{"ticket": newTicketView(*storedTicket)})
}

func (h *Handler) handleAddComment(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")
	var req struct {
		Body string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := decodeJSONBody(r.Body, &req); err != nil {
		h.writeError(w, err, http.StatusBadRequest)
		return
	}
	commentBody := strings.TrimSpace(req.Body)
	if commentBody == "" {
		h.writeError(w, fmt.Errorf("comment body must not be empty"), http.StatusBadRequest)
		return
	}
	if err := h.service.AddUserComment(r.Context(), ticketID, commentBody); err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}
	comments, err := h.service.ListComments(r.Context(), ticketID)
	if err != nil {
		h.writeError(w, err, http.StatusInternalServerError)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{"comments": ensureCommentViews(comments)})
}

func (h *Handler) handleRequeueTicket(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("id")

	existing, err := h.service.GetTicket(r.Context(), ticketID)
	if err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}
	if !runAgainAllowed(*existing) {
		h.writeError(w, fmt.Errorf("ticket %q is not eligible for requeue", ticketID), http.StatusConflict)
		return
	}

	if err := h.service.RequeueTicket(r.Context(), ticketID); err != nil {
		h.writeError(w, err, statusFor(err))
		return
	}
	ticket, err := h.service.GetTicket(r.Context(), ticketID)
	if err != nil {
		h.writeError(w, err, http.StatusInternalServerError)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"ticket": newTicketView(*ticket)})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		h.logger.Error("local UI response encoding failed", "error", err)
	}
}

func (h *Handler) writeError(w http.ResponseWriter, err error, status int) {
	h.writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeJSONBody(body io.ReadCloser, dest any) error {
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("request body must not be empty")
		}
		return fmt.Errorf("decoding request body: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain a single JSON value")
	}

	return nil
}

func trimSlice(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func ensureCommentViews(comments []localticket.StoredComment) []commentView {
	if comments == nil {
		return []commentView{}
	}
	views := make([]commentView, 0, len(comments))
	for _, comment := range comments {
		views = append(views, newCommentView(comment))
	}
	return views
}

func ensureTicketViews(tickets []localticket.StoredTicket) []ticketView {
	if tickets == nil {
		return []ticketView{}
	}
	views := make([]ticketView, 0, len(tickets))
	for _, ticket := range tickets {
		views = append(views, newTicketView(ticket))
	}
	return views
}

func newCommentView(comment localticket.StoredComment) commentView {
	return commentView{
		ID:        comment.ID,
		TicketID:  comment.TicketID,
		Kind:      commentKindLabel(comment.Kind),
		Body:      commentBodyForUI(comment),
		CreatedAt: comment.CreatedAt,
	}
}

func commentBodyForUI(comment localticket.StoredComment) string {
	if comment.Kind != localticket.CommentKindSystem {
		return comment.Body
	}

	body := strings.ReplaceAll(comment.Body, "Task completed successfully.", "Run completed successfully.")
	body = strings.ReplaceAll(body, "Task failed.", "Run failed.")
	return body
}

func commentKindLabel(kind localticket.CommentKind) string {
	switch kind {
	case localticket.CommentKindSystem:
		return "System"
	case localticket.CommentKindUser:
		return "Note"
	default:
		return "Activity"
	}
}

func newTicketView(ticket localticket.StoredTicket) ticketView {
	return ticketView{
		Ticket:          ticket.Ticket,
		TrackerStatus:   trackerStatusForUI(ticket),
		RunAgainAllowed: runAgainAllowed(ticket),
		RunOutcome:      runOutcomeForUI(ticket),
		RunSummary:      runSummaryForUI(ticket),
		NeedsAttention:  needsAttention(ticket),
		FailureReason:   ticket.FailureReason,
		CreatedAt:       ticket.CreatedAt,
		UpdatedAt:       ticket.UpdatedAt,
		InProgressAt:    ticket.InProgressAt,
		CompletedAt:     ticket.CompletedAt,
		FailedAt:        ticket.FailedAt,
	}
}

func trackerStatusForUI(ticket localticket.StoredTicket) trackerStatus {
	switch ticket.Status {
	case localticket.StatusDone:
		return trackerStatusDone
	case localticket.StatusInProgress:
		return trackerStatusInProgress
	default:
		return trackerStatusTodo
	}
}

func runAgainAllowed(ticket localticket.StoredTicket) bool {
	return ticket.Status == localticket.StatusDone || ticket.RunState == localticket.RunStateFailed
}

func runOutcomeForUI(ticket localticket.StoredTicket) runOutcome {
	switch ticket.RunState {
	case localticket.RunStateSucceeded:
		return runOutcomeSucceeded
	case localticket.RunStateFailed:
		return runOutcomeFailed
	case localticket.RunStateRunning:
		return runOutcomeRunning
	default:
		return runOutcomeIdle
	}
}

func runSummaryForUI(ticket localticket.StoredTicket) string {
	if ticket.Result != nil {
		return strings.TrimSpace(ticket.Result.Summary)
	}
	return ""
}

func needsAttention(ticket localticket.StoredTicket) bool {
	return runOutcomeForUI(ticket) == runOutcomeFailed
}

func statusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "no rows in result set"):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "already exists"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "constraint failed"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "in progress"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "currently running"):
		return http.StatusConflict
	default:
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return http.StatusBadRequest
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return http.StatusBadRequest
		}
		if errors.Is(err, io.EOF) {
			return http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "must not be empty") {
			return http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "must contain a single JSON value") {
			return http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "unknown field") {
			return http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "decoding request body") {
			return http.StatusBadRequest
		}
		return http.StatusInternalServerError
	}
}
