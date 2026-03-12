package localui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
	localticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/local"
)

type stubService struct {
	listTicketsFunc   func(ctx context.Context) ([]localticket.StoredTicket, error)
	getTicketFunc     func(ctx context.Context, id string) (*localticket.StoredTicket, error)
	listCommentsFunc  func(ctx context.Context, id string) ([]localticket.StoredComment, error)
	createTicketFunc  func(ctx context.Context, ticket ticketing.Ticket) error
	requeueTicketFunc func(ctx context.Context, id string) error
	addCommentFunc    func(ctx context.Context, id string, comment string) error
}

func (s stubService) ListTickets(ctx context.Context) ([]localticket.StoredTicket, error) {
	if s.listTicketsFunc != nil {
		return s.listTicketsFunc(ctx)
	}
	return nil, nil
}

func (s stubService) GetTicket(ctx context.Context, id string) (*localticket.StoredTicket, error) {
	if s.getTicketFunc != nil {
		return s.getTicketFunc(ctx, id)
	}
	return nil, nil
}

func (s stubService) ListComments(ctx context.Context, id string) ([]localticket.StoredComment, error) {
	if s.listCommentsFunc != nil {
		return s.listCommentsFunc(ctx, id)
	}
	return nil, nil
}

func (s stubService) CreateTicket(ctx context.Context, ticket ticketing.Ticket) error {
	if s.createTicketFunc != nil {
		return s.createTicketFunc(ctx, ticket)
	}
	return nil
}

func (s stubService) RequeueTicket(ctx context.Context, id string) error {
	if s.requeueTicketFunc != nil {
		return s.requeueTicketFunc(ctx, id)
	}
	return nil
}

func (s stubService) AddUserComment(ctx context.Context, id string, comment string) error {
	if s.addCommentFunc != nil {
		return s.addCommentFunc(ctx, id, comment)
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestBackendAndHandler(t *testing.T) (*localticket.Backend, http.Handler) {
	t.Helper()

	backend, err := localticket.New(localticket.Config{StorePath: ":memory:"}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	require.NoError(t, backend.CreateTicket(context.Background(), ticketing.Ticket{
		ID:          "LOCAL-1",
		Title:       "First local ticket",
		Description: "Test fixture",
		TicketType:  "bug",
	}))

	handler, err := NewHandler(testLogger(), backend)
	require.NoError(t, err)
	return backend, handler
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	_, handler := newTestBackendAndHandler(t)
	return handler
}

func TestHandler_ServesIndex(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Osmia Local Tickets")
}

func TestHandler_ListsTickets(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Tickets []struct {
			Ticket        ticketing.Ticket `json:"ticket"`
			TrackerStatus string           `json:"tracker_status"`
			RunOutcome    string           `json:"run_outcome"`
		} `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Tickets, 1)
	assert.Equal(t, "LOCAL-1", payload.Tickets[0].Ticket.ID)
	assert.Equal(t, "todo", payload.Tickets[0].TrackerStatus)
	assert.Equal(t, "idle", payload.Tickets[0].RunOutcome)
}

func TestHandler_ListsTicketsAsEmptyArrayWhenServiceReturnsNil(t *testing.T) {
	handler, err := NewHandler(testLogger(), stubService{})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"tickets":[]}`, rec.Body.String())
}

func TestHandler_CreatesCommentsAndRequeuesTickets(t *testing.T) {
	backend, handler := newTestBackendAndHandler(t)

	createBody := bytes.NewBufferString(`{
		"id":"LOCAL-2",
		"title":"Second local ticket",
		"description":"Created via UI",
		"ticket_type":"feature",
		"labels":["osmia","ui"]
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/tickets", createBody)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	commentReq := httptest.NewRequest(http.MethodPost, "/api/tickets/LOCAL-2/comments", bytes.NewBufferString(`{"body":"Operator note"}`))
	commentRec := httptest.NewRecorder()
	handler.ServeHTTP(commentRec, commentReq)
	require.Equal(t, http.StatusCreated, commentRec.Code)

	require.NoError(t, backend.MarkFailed(context.Background(), "LOCAL-2", "worker stopped"))

	requeueReq := httptest.NewRequest(http.MethodPost, "/api/tickets/LOCAL-2/requeue", nil)
	requeueRec := httptest.NewRecorder()
	handler.ServeHTTP(requeueRec, requeueReq)
	require.Equal(t, http.StatusOK, requeueRec.Code)

	var requeuePayload struct {
		Ticket struct {
			Ticket          ticketing.Ticket `json:"ticket"`
			TrackerStatus   string           `json:"tracker_status"`
			RunAgainAllowed bool             `json:"run_again_allowed"`
			RunOutcome      string           `json:"run_outcome"`
		} `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal(requeueRec.Body.Bytes(), &requeuePayload))
	assert.Equal(t, "LOCAL-2", requeuePayload.Ticket.Ticket.ID)
	assert.Equal(t, "todo", requeuePayload.Ticket.TrackerStatus)
	assert.Equal(t, "idle", requeuePayload.Ticket.RunOutcome)
	assert.False(t, requeuePayload.Ticket.RunAgainAllowed)

	commentsReq := httptest.NewRequest(http.MethodGet, "/api/tickets/LOCAL-2/comments", nil)
	commentsRec := httptest.NewRecorder()
	handler.ServeHTTP(commentsRec, commentsReq)
	require.Equal(t, http.StatusOK, commentsRec.Code)

	var commentsPayload struct {
		Comments []struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"comments"`
	}
	require.NoError(t, json.Unmarshal(commentsRec.Body.Bytes(), &commentsPayload))
	require.Len(t, commentsPayload.Comments, 2)
	assert.Equal(t, "Note", commentsPayload.Comments[0].Kind)
	assert.Equal(t, "Operator note", commentsPayload.Comments[0].Body)
	assert.Equal(t, "System", commentsPayload.Comments[1].Kind)
}

func TestHandler_ListsCommentsAsEmptyArrayWhenServiceReturnsNil(t *testing.T) {
	handler, err := NewHandler(testLogger(), stubService{
		listCommentsFunc: func(context.Context, string) ([]localticket.StoredComment, error) {
			return nil, nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/LOCAL-1/comments", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"comments":[]}`, rec.Body.String())
}

func TestHandler_ReturnsNotFoundForMissingTicket(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/DOES-NOT-EXIST", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_ReturnsConflictForDuplicateTicket(t *testing.T) {
	handler := newTestHandler(t)

	createBody := bytes.NewBufferString(`{
		"id":"LOCAL-1",
		"title":"Duplicate local ticket"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", createBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandler_AcceptsEscapedTicketIDsInAPIPaths(t *testing.T) {
	backend, err := localticket.New(localticket.Config{StorePath: ":memory:"}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	ticketID := "LOCAL/1?#"
	require.NoError(t, backend.CreateTicket(context.Background(), ticketing.Ticket{
		ID:    ticketID,
		Title: "Escaped local ticket",
	}))

	handler, err := NewHandler(testLogger(), backend)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/"+url.PathEscape(ticketID), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Ticket localticket.StoredTicket `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, ticketID, payload.Ticket.Ticket.ID)
}

func TestHandler_RejectsEmptyCommentBody(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/tickets/LOCAL-1/comments",
		bytes.NewBufferString(`{"body":"   "}`),
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_RejectsUnknownCreateFields(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/tickets",
		bytes.NewBufferString(`{"id":"LOCAL-2","title":"Valid title","unexpected":"value"}`),
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_ReturnsInternalServerErrorForUnexpectedServiceFailure(t *testing.T) {
	handler, err := NewHandler(testLogger(), stubService{
		getTicketFunc: func(context.Context, string) (*localticket.StoredTicket, error) {
			return nil, errors.New("boom")
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/LOCAL-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
