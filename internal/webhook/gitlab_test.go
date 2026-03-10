package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGitLab(t *testing.T) {
	secret := "test-gitlab-secret"

	issuePayload := glWebhookPayload{
		ObjectKind: "issue",
		ObjectAttributes: glObjectAttributes{
			IID:    7,
			Title:  "Fix pipeline",
			Desc:   "CI pipeline is broken",
			URL:    "https://gitlab.com/owner/repo/-/issues/7",
			Action: "open",
		},
		Project: glProject{
			WebURL: "https://gitlab.com/owner/repo",
			PathNS: "owner/repo",
		},
		Labels: []glLabel{{Title: "osmia"}, {Title: "ci"}},
	}

	mrPayload := glWebhookPayload{
		ObjectKind: "merge_request",
		ObjectAttributes: glObjectAttributes{
			IID:   15,
			Title: "Add feature",
			Desc:  "New feature implementation",
			URL:   "https://gitlab.com/owner/repo/-/merge_requests/15",
		},
		Project: glProject{
			WebURL: "https://gitlab.com/owner/repo",
			PathNS: "owner/repo",
		},
		Labels: []glLabel{{Title: "feature"}},
	}

	tests := []struct {
		name       string
		payload    any
		token      string
		wantStatus int
		wantCalls  int
		wantType   string
	}{
		{
			name:       "valid issue event",
			payload:    issuePayload,
			token:      secret,
			wantStatus: http.StatusOK,
			wantCalls:  1,
			wantType:   "issue",
		},
		{
			name:       "valid merge request event",
			payload:    mrPayload,
			token:      secret,
			wantStatus: http.StatusOK,
			wantCalls:  1,
			wantType:   "merge_request",
		},
		{
			name:       "invalid token",
			payload:    issuePayload,
			token:      "wrong-token",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing token",
			payload:    issuePayload,
			token:      "",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name: "unsupported event type ignored",
			payload: glWebhookPayload{
				ObjectKind: "pipeline",
			},
			token:      secret,
			wantStatus: http.StatusOK,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			srv := NewServer(testLogger(), mock, WithSecret("gitlab", secret))

			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("X-Gitlab-Token", tc.token)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				call := mock.calls[0]
				assert.Equal(t, "gitlab", call.source)
				require.Len(t, call.tickets, 1)
				assert.Equal(t, tc.wantType, call.tickets[0].TicketType)
			}
		})
	}
}

func TestHandleGitLab_IssueFields(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithSecret("gitlab", secret))

	payload := glWebhookPayload{
		ObjectKind: "issue",
		ObjectAttributes: glObjectAttributes{
			IID:   42,
			Title: "Test issue",
			Desc:  "Issue description",
			URL:   "https://gitlab.com/o/r/-/issues/42",
		},
		Project: glProject{
			WebURL: "https://gitlab.com/o/r",
			PathNS: "o/r",
		},
		Labels: []glLabel{{Title: "bug"}, {Title: "urgent"}},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", secret)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, mock.calls, 1)

	ticket := mock.calls[0].tickets[0]
	assert.Equal(t, "42", ticket.ID)
	assert.Equal(t, "Test issue", ticket.Title)
	assert.Equal(t, "Issue description", ticket.Description)
	assert.Equal(t, "issue", ticket.TicketType)
	assert.Equal(t, []string{"bug", "urgent"}, ticket.Labels)
	assert.Equal(t, "https://gitlab.com/o/r", ticket.RepoURL)
	assert.Equal(t, "https://gitlab.com/o/r/-/issues/42", ticket.ExternalURL)
}

func TestHandleGitLab_MalformedJSON(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithSecret("gitlab", secret))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", secret)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mock.calls)
}

func TestHandleGitLab_MissingSecret(t *testing.T) {
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock) // no gitlab secret

	payload := glWebhookPayload{ObjectKind: "issue"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", "any-token")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandleGitLab_HandlerError(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{err: fmt.Errorf("handler failed")}
	srv := NewServer(testLogger(), mock, WithSecret("gitlab", secret))

	payload := glWebhookPayload{
		ObjectKind: "issue",
		ObjectAttributes: glObjectAttributes{
			IID:   1,
			Title: "Test",
			URL:   "https://gitlab.com/o/r/-/issues/1",
		},
		Project: glProject{WebURL: "https://gitlab.com/o/r", PathNS: "o/r"},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", secret)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
