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

func TestHandleGitHub(t *testing.T) {
	secret := "test-github-secret"

	validPayload := ghWebhookPayload{
		Action: "opened",
		Issue: ghIssue{
			Number:  42,
			Title:   "Fix login bug",
			Body:    "The login page crashes",
			HTMLURL: "https://github.com/owner/repo/issues/42",
			Labels:  []ghLabel{{Name: "osmia"}, {Name: "bug"}},
		},
		Repo: ghRepo{
			FullName: "owner/repo",
			HTMLURL:  "https://github.com/owner/repo",
		},
	}

	tests := []struct {
		name       string
		payload    any
		event      string
		action     string
		secret     string
		sigFunc    func([]byte) string
		wantStatus int
		wantCalls  int
	}{
		{
			name:    "valid opened issue",
			payload: validPayload,
			event:   "issues",
			action:  "opened",
			secret:  secret,
			sigFunc: func(b []byte) string {
				return computeGitHubSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name: "valid labelled issue",
			payload: func() ghWebhookPayload {
				p := validPayload
				p.Action = "labeled"
				return p
			}(),
			event:  "issues",
			secret: secret,
			sigFunc: func(b []byte) string {
				return computeGitHubSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "invalid signature",
			payload:    validPayload,
			event:      "issues",
			secret:     secret,
			sigFunc:    func(_ []byte) string { return "sha256=deadbeef" },
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing signature",
			payload:    validPayload,
			event:      "issues",
			secret:     secret,
			sigFunc:    func(_ []byte) string { return "" },
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:    "non-issue event ignored",
			payload: validPayload,
			event:   "push",
			secret:  secret,
			sigFunc: func(b []byte) string {
				return computeGitHubSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  0,
		},
		{
			name: "closed action ignored",
			payload: func() ghWebhookPayload {
				p := validPayload
				p.Action = "closed"
				return p
			}(),
			event:  "issues",
			secret: secret,
			sigFunc: func(b []byte) string {
				return computeGitHubSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  0,
		},
		{
			name:       "missing secret returns 500",
			payload:    validPayload,
			event:      "issues",
			secret:     "", // no secret configured
			sigFunc:    func(_ []byte) string { return "sha256=abc" },
			wantStatus: http.StatusInternalServerError,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}

			opts := []Option{}
			if tc.secret != "" {
				opts = append(opts, WithSecret("github", tc.secret))
			}
			srv := NewServer(testLogger(), mock, opts...)

			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			sig := tc.sigFunc(body)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GitHub-Event", tc.event)
			if sig != "" {
				req.Header.Set("X-Hub-Signature-256", sig)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				call := mock.calls[0]
				assert.Equal(t, "github", call.source)
				require.Len(t, call.tickets, 1)
				assert.Equal(t, "42", call.tickets[0].ID)
				assert.Equal(t, "Fix login bug", call.tickets[0].Title)
				assert.Equal(t, "The login page crashes", call.tickets[0].Description)
				assert.Equal(t, "issue", call.tickets[0].TicketType)
				assert.Equal(t, []string{"osmia", "bug"}, call.tickets[0].Labels)
				assert.Equal(t, "https://github.com/owner/repo", call.tickets[0].RepoURL)
				assert.Equal(t, "https://github.com/owner/repo/issues/42", call.tickets[0].ExternalURL)
			}
		})
	}
}

func TestHandleGitHub_MalformedJSON(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithSecret("github", secret))

	body := []byte(`{invalid json`)
	sig := computeGitHubSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mock.calls)
}

func TestHandleGitHub_HandlerError(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{err: fmt.Errorf("handler failed")}
	srv := NewServer(testLogger(), mock, WithSecret("github", secret))

	payload := ghWebhookPayload{
		Action: "opened",
		Issue: ghIssue{
			Number:  1,
			Title:   "Test",
			HTMLURL: "https://github.com/o/r/issues/1",
		},
		Repo: ghRepo{FullName: "o/r", HTMLURL: "https://github.com/o/r"},
	}

	body, _ := json.Marshal(payload)
	sig := computeGitHubSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandleGitHub_TriggerLabelGating(t *testing.T) {
	secret := "test-secret"

	tests := []struct {
		name          string
		issueLabels   []ghLabel
		triggerLabels []string
		wantCalls     int
	}{
		{
			name:          "issue with trigger label is forwarded",
			issueLabels:   []ghLabel{{Name: "osmia"}, {Name: "bug"}},
			triggerLabels: []string{"osmia"},
			wantCalls:     1,
		},
		{
			name:          "issue without trigger label is dropped",
			issueLabels:   []ghLabel{{Name: "bug"}},
			triggerLabels: []string{"osmia"},
			wantCalls:     0,
		},
		{
			name:          "no trigger labels configured — all issues forwarded",
			issueLabels:   []ghLabel{{Name: "bug"}},
			triggerLabels: nil,
			wantCalls:     1,
		},
		{
			name:          "multiple trigger labels — any match is sufficient",
			issueLabels:   []ghLabel{{Name: "enhancement"}},
			triggerLabels: []string{"osmia", "enhancement"},
			wantCalls:     1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}

			opts := []Option{WithSecret("github", secret)}
			if len(tc.triggerLabels) > 0 {
				opts = append(opts, WithGitHubTriggerLabels(tc.triggerLabels))
			}
			srv := NewServer(testLogger(), mock, opts...)

			payload := ghWebhookPayload{
				Action: "opened",
				Issue: ghIssue{
					Number:  1,
					Title:   "Test issue",
					HTMLURL: "https://github.com/o/r/issues/1",
					Labels:  tc.issueLabels,
				},
				Repo: ghRepo{FullName: "o/r", HTMLURL: "https://github.com/o/r"},
			}

			body, err := json.Marshal(payload)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GitHub-Event", "issues")
			req.Header.Set("X-Hub-Signature-256", computeGitHubSignature(body, secret))

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)
		})
	}
}

func TestValidateGitHubSignature(t *testing.T) {
	body := []byte(`{"test": true}`)
	secret := "my-secret"

	tests := []struct {
		name string
		sig  string
		want bool
	}{
		{
			name: "valid signature",
			sig:  computeGitHubSignature(body, secret),
			want: true,
		},
		{
			name: "empty signature",
			sig:  "",
			want: false,
		},
		{
			name: "wrong prefix",
			sig:  "md5=abc123",
			want: false,
		},
		{
			name: "invalid hex",
			sig:  "sha256=zzzz",
			want: false,
		},
		{
			name: "wrong secret",
			sig:  computeGitHubSignature(body, "wrong-secret"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateGitHubSignature(body, tc.sig, secret)
			assert.Equal(t, tc.want, got)
		})
	}
}
