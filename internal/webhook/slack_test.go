package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleSlack(t *testing.T) {
	secret := "test-slack-secret"
	now := time.Now()
	tsStr := strconv.FormatInt(now.Unix(), 10)

	validPayload := slackInteractionPayload{
		Type: "block_actions",
		Actions: []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		}{
			{ActionID: "some_action", Value: "test"},
		},
		User: struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}{ID: "U123", Username: "testuser"},
		Channel: struct {
			ID string `json:"id"`
		}{ID: "C456"},
	}

	tests := []struct {
		name       string
		payload    any
		timestamp  string
		sigFunc    func([]byte, string) string
		secret     string
		wantStatus int
		wantCalls  int
	}{
		{
			name:      "valid interaction",
			payload:   validPayload,
			timestamp: tsStr,
			sigFunc: func(b []byte, ts string) string {
				return computeSlackSignature(b, ts, secret)
			},
			secret:     secret,
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "invalid signature",
			payload:    validPayload,
			timestamp:  tsStr,
			sigFunc:    func(_ []byte, _ string) string { return "v0=deadbeef" },
			secret:     secret,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing timestamp",
			payload:    validPayload,
			timestamp:  "",
			sigFunc:    func(_ []byte, _ string) string { return "v0=abc" },
			secret:     secret,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:      "stale timestamp (replay attack)",
			payload:   validPayload,
			timestamp: strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10),
			sigFunc: func(b []byte, ts string) string {
				return computeSlackSignature(b, ts, secret)
			},
			secret:     secret,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing secret returns 500",
			payload:    validPayload,
			timestamp:  tsStr,
			sigFunc:    func(_ []byte, _ string) string { return "v0=abc" },
			secret:     "", // no secret configured
			wantStatus: http.StatusInternalServerError,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}

			opts := []Option{}
			if tc.secret != "" {
				opts = append(opts, WithSecret("slack", tc.secret))
			}
			srv := NewServer(testLogger(), mock, opts...)

			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			sig := tc.sigFunc(body, tc.timestamp)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.timestamp != "" {
				req.Header.Set("X-Slack-Request-Timestamp", tc.timestamp)
			}
			if sig != "" {
				req.Header.Set("X-Slack-Signature", sig)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				call := mock.calls[0]
				assert.Equal(t, "slack", call.source)
				require.Len(t, call.tickets, 1)
				assert.Equal(t, "some_action", call.tickets[0].ID)
				assert.Equal(t, "test", call.tickets[0].Title)
				assert.Equal(t, "slack_interaction", call.tickets[0].TicketType)
			}
		})
	}
}

// mockApprovalHandler records approval callback calls for testing.
type mockApprovalHandler struct {
	calls []approvalCall
}

type approvalCall struct {
	taskRunID string
	approved  bool
	responder string
}

func (m *mockApprovalHandler) HandleApprovalCallback(_ context.Context, taskRunID string, approved bool, responder string) error {
	m.calls = append(m.calls, approvalCall{taskRunID: taskRunID, approved: approved, responder: responder})
	return nil
}

func TestHandleSlack_ApprovalCallbacks(t *testing.T) {
	// Approval callbacks (osmia_approval_*) must be acknowledged with 200 OK
	// but must NOT be forwarded to the event handler as synthetic tickets.
	// When an ApprovalHandler is configured, it receives the callback instead.
	secret := "test-secret"
	now := time.Now()
	tsStr := strconv.FormatInt(now.Unix(), 10)

	makePayload := func(actionID, value string) slackInteractionPayload {
		return slackInteractionPayload{
			Type: "block_actions",
			Actions: []struct {
				ActionID string `json:"action_id"`
				Value    string `json:"value"`
			}{
				{ActionID: actionID, Value: value},
			},
			User: struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			}{ID: "U1", Username: "alice"},
		}
	}

	tests := []struct {
		name              string
		actionID          string
		value             string
		wantEventCalls    int
		wantApprovalCalls int
		wantApproved      bool
		wantTaskRunID     string
	}{
		{
			name:              "approval callback routed to handler",
			actionID:          "osmia_approval_tr-42-1_0",
			value:             "approve",
			wantEventCalls:    0,
			wantApprovalCalls: 1,
			wantApproved:      true,
			wantTaskRunID:     "tr-42-1",
		},
		{
			name:              "rejection callback routed to handler",
			actionID:          "osmia_approval_tr-42-1_0",
			value:             "reject",
			wantEventCalls:    0,
			wantApprovalCalls: 1,
			wantApproved:      false,
			wantTaskRunID:     "tr-42-1",
		},
		{
			name:              "deny value treated as rejection",
			actionID:          "osmia_approval_tr-99_0",
			value:             "deny",
			wantEventCalls:    0,
			wantApprovalCalls: 1,
			wantApproved:      false,
			wantTaskRunID:     "tr-99",
		},
		{
			name:              "non-approval action is forwarded",
			actionID:          "some_other_action",
			value:             "clicked",
			wantEventCalls:    1,
			wantApprovalCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			ah := &mockApprovalHandler{}
			srv := NewServer(testLogger(), mock, WithSecret("slack", secret), WithApprovalHandler(ah))

			payload := makePayload(tc.actionID, tc.value)
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			sig := computeSlackSignature(body, tsStr, secret)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Slack-Request-Timestamp", tsStr)
			req.Header.Set("X-Slack-Signature", sig)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Len(t, mock.calls, tc.wantEventCalls)
			assert.Len(t, ah.calls, tc.wantApprovalCalls)

			if tc.wantApprovalCalls > 0 {
				assert.Equal(t, tc.wantTaskRunID, ah.calls[0].taskRunID)
				assert.Equal(t, tc.wantApproved, ah.calls[0].approved)
				assert.Equal(t, "alice", ah.calls[0].responder)
			}
		})
	}
}

func TestHandleSlack_ApprovalCallbackWithoutHandler(t *testing.T) {
	// When no ApprovalHandler is configured, approval callbacks are
	// acknowledged but not forwarded to the event handler.
	secret := "test-secret"
	now := time.Now()
	tsStr := strconv.FormatInt(now.Unix(), 10)

	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithSecret("slack", secret))

	payload := slackInteractionPayload{
		Type: "block_actions",
		Actions: []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		}{
			{ActionID: "osmia_approval_tr-1_0", Value: "approve"},
		},
		User: struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}{ID: "U1", Username: "bob"},
	}

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	sig := computeSlackSignature(body, tsStr, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", tsStr)
	req.Header.Set("X-Slack-Signature", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, mock.calls)
}

func TestHandleSlack_MalformedJSON(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithSecret("slack", secret))

	body := []byte(`{invalid`)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	sig := computeSlackSignature(body, tsStr, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", tsStr)
	req.Header.Set("X-Slack-Signature", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSlack_HandlerError(t *testing.T) {
	secret := "test-secret"
	mock := &mockEventHandler{err: fmt.Errorf("handler failed")}
	srv := NewServer(testLogger(), mock, WithSecret("slack", secret))

	payload := slackInteractionPayload{
		Type: "block_actions",
		Actions: []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		}{
			{ActionID: "test", Value: "approve"},
		},
	}

	body, _ := json.Marshal(payload)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	sig := computeSlackSignature(body, tsStr, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", tsStr)
	req.Header.Set("X-Slack-Signature", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestValidateSlackSignature(t *testing.T) {
	body := []byte(`{"test": true}`)
	secret := "my-secret"
	ts := "1234567890"

	tests := []struct {
		name string
		sig  string
		want bool
	}{
		{
			name: "valid signature",
			sig:  computeSlackSignature(body, ts, secret),
			want: true,
		},
		{
			name: "empty signature",
			sig:  "",
			want: false,
		},
		{
			name: "wrong prefix",
			sig:  "v1=abc123",
			want: false,
		},
		{
			name: "invalid hex",
			sig:  "v0=zzzz",
			want: false,
		},
		{
			name: "wrong secret",
			sig:  computeSlackSignature(body, ts, "wrong-secret"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateSlackSignature(body, ts, tc.sig, secret)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractFormValue(t *testing.T) {
	tests := []struct {
		name string
		body string
		key  string
		want string
	}{
		{
			name: "simple value",
			body: "key=value",
			key:  "key",
			want: "value",
		},
		{
			name: "payload in form",
			body: "token=abc&payload=%7B%22test%22%3Atrue%7D",
			key:  "payload",
			want: `{"test":true}`,
		},
		{
			name: "missing key",
			body: "other=value",
			key:  "payload",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			key:  "key",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFormValue([]byte(tc.body), tc.key)
			assert.Equal(t, tc.want, got)
		})
	}
}
