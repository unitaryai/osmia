package slack

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/pkg/plugin/approval"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newTestBackend(t *testing.T, serverURL string) *SlackApprovalBackend {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewSlackApprovalBackend("C12345", "xoxb-test-token", logger).
		WithAPIURL(serverURL)
}

func sampleTicket() ticketing.Ticket {
	return ticketing.Ticket{
		ID:          "TICKET-42",
		Title:       "Fix broken login flow",
		Description: "Users cannot log in when using SSO",
		ExternalURL: "https://github.com/org/repo/issues/42",
	}
}

func TestSlackApprovalBackend_ImplementsInterface(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var b approval.Backend = NewSlackApprovalBackend("C12345", "xoxb-test", logger)
	assert.NotNil(t, b)
}

func TestSlackApprovalBackend_Name(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := NewSlackApprovalBackend("C12345", "xoxb-test", logger)
	assert.Equal(t, "slack", b.Name())
}

func TestSlackApprovalBackend_InterfaceVersion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := NewSlackApprovalBackend("C12345", "xoxb-test", logger)
	assert.Equal(t, approval.InterfaceVersion, b.InterfaceVersion())
}

func TestSlackApprovalBackend_RequestApproval(t *testing.T) {
	tests := []struct {
		name       string
		question   string
		ticket     ticketing.Ticket
		taskRunID  string
		options    []string
		apiResp    slackResponse
		statusCode int
		wantErr    bool
	}{
		{
			name:      "successful approval request",
			question:  "Should we proceed with the merge?",
			ticket:    sampleTicket(),
			taskRunID: "run-001",
			options:   []string{"approve", "reject"},
			apiResp:   slackResponse{OK: true, TS: "1234567890.123456"},
		},
		{
			name:      "multiple options",
			question:  "Choose an action",
			ticket:    sampleTicket(),
			taskRunID: "run-002",
			options:   []string{"approve", "reject", "skip"},
			apiResp:   slackResponse{OK: true, TS: "1234567890.654321"},
		},
		{
			name:      "slack API error",
			question:  "Approve?",
			ticket:    sampleTicket(),
			taskRunID: "run-003",
			options:   []string{"approve", "reject"},
			apiResp:   slackResponse{OK: false, Error: "channel_not_found"},
			wantErr:   true,
		},
		{
			name:       "HTTP error",
			question:   "Approve?",
			ticket:     sampleTicket(),
			taskRunID:  "run-004",
			options:    []string{"approve"},
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statusCode := tt.statusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}

			var capturedMsg slackMessage

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/chat.postMessage", r.URL.Path)
				assert.Equal(t, "Bearer xoxb-test-token", r.Header.Get("Authorization"))

				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedMsg))

				w.WriteHeader(statusCode)
				resp, _ := json.Marshal(tt.apiResp)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			b := newTestBackend(t, server.URL)
			err := b.RequestApproval(context.Background(), tt.question, tt.ticket, tt.taskRunID, tt.options)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify message contains the question and action buttons.
			assert.Equal(t, "C12345", capturedMsg.Channel)
			blocksJSON, _ := json.Marshal(capturedMsg.Blocks)
			blocksStr := string(blocksJSON)
			assert.Contains(t, blocksStr, tt.question)

			// Verify each option appears as a button value.
			for _, opt := range tt.options {
				assert.Contains(t, blocksStr, opt)
			}

			// Verify the pending request was stored.
			ts, ok := b.pendingRequests.Load(tt.taskRunID)
			require.True(t, ok, "pending request should be stored")
			assert.Equal(t, tt.apiResp.TS, ts)
		})
	}
}

func TestSlackApprovalBackend_CancelPending(t *testing.T) {
	tests := []struct {
		name         string
		taskRunID    string
		setupPending bool
		apiResp      slackResponse
		wantAPICall  bool
		wantErr      bool
	}{
		{
			name:         "cancel existing pending request",
			taskRunID:    "run-001",
			setupPending: true,
			apiResp:      slackResponse{OK: true, TS: "1234567890.123456"},
			wantAPICall:  true,
		},
		{
			name:         "cancel non-existent request (no-op)",
			taskRunID:    "run-999",
			setupPending: false,
			wantAPICall:  false,
		},
		{
			name:         "slack API error on cancel",
			taskRunID:    "run-002",
			setupPending: true,
			apiResp:      slackResponse{OK: false, Error: "message_not_found"},
			wantAPICall:  true,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiCalled := false

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				apiCalled = true
				assert.Equal(t, "/chat.update", r.URL.Path)
				assert.Equal(t, "Bearer xoxb-test-token", r.Header.Get("Authorization"))

				// Verify the update payload.
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				var updateMsg slackUpdateMessage
				require.NoError(t, json.Unmarshal(body, &updateMsg))
				assert.Equal(t, "C12345", updateMsg.Channel)
				assert.Contains(t, updateMsg.Text, "cancelled")

				w.WriteHeader(http.StatusOK)
				resp, _ := json.Marshal(tt.apiResp)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			b := newTestBackend(t, server.URL)

			if tt.setupPending {
				b.pendingRequests.Store(tt.taskRunID, "1234567890.123456")
			}

			err := b.CancelPending(context.Background(), tt.taskRunID)

			assert.Equal(t, tt.wantAPICall, apiCalled, "API call expectation mismatch")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify pending request was removed.
			_, ok := b.pendingRequests.Load(tt.taskRunID)
			assert.False(t, ok, "pending request should be removed after cancel")
		})
	}
}

func TestSlackApprovalBackend_HandleCallback(t *testing.T) {
	tests := []struct {
		name         string
		payload      InteractionPayload
		setupTS      string
		taskRunID    string
		wantApproved bool
		wantErr      bool
	}{
		{
			name: "approve callback",
			payload: InteractionPayload{
				Type: "block_actions",
				Actions: []struct {
					ActionID string `json:"action_id"`
					Value    string `json:"value"`
				}{
					{ActionID: "robodev_approval_run-001_0", Value: "approve"},
				},
				User: struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				}{ID: "U12345", Username: "testuser"},
				Message: struct {
					TS string `json:"ts"`
				}{TS: "1234567890.123456"},
			},
			setupTS:      "1234567890.123456",
			taskRunID:    "run-001",
			wantApproved: true,
		},
		{
			name: "reject callback",
			payload: InteractionPayload{
				Type: "block_actions",
				Actions: []struct {
					ActionID string `json:"action_id"`
					Value    string `json:"value"`
				}{
					{ActionID: "robodev_approval_run-002_1", Value: "reject"},
				},
				User: struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				}{ID: "U67890", Username: "reviewer"},
				Message: struct {
					TS string `json:"ts"`
				}{TS: "1234567890.654321"},
			},
			setupTS:      "1234567890.654321",
			taskRunID:    "run-002",
			wantApproved: false,
		},
		{
			name: "deny callback",
			payload: InteractionPayload{
				Type: "block_actions",
				Actions: []struct {
					ActionID string `json:"action_id"`
					Value    string `json:"value"`
				}{
					{ActionID: "robodev_approval_run-003_1", Value: "deny"},
				},
				User: struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				}{ID: "U11111", Username: "admin"},
				Message: struct {
					TS string `json:"ts"`
				}{TS: "1234567890.111111"},
			},
			setupTS:      "1234567890.111111",
			taskRunID:    "run-003",
			wantApproved: false,
		},
		{
			name: "unknown message timestamp",
			payload: InteractionPayload{
				Type: "block_actions",
				Actions: []struct {
					ActionID string `json:"action_id"`
					Value    string `json:"value"`
				}{
					{ActionID: "some_action", Value: "approve"},
				},
				Message: struct {
					TS string `json:"ts"`
				}{TS: "9999999999.999999"},
			},
			wantErr: true,
		},
		{
			name: "empty actions",
			payload: InteractionPayload{
				Type:    "block_actions",
				Actions: nil,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			b := NewSlackApprovalBackend("C12345", "xoxb-test", logger)

			if tt.taskRunID != "" {
				b.pendingRequests.Store(tt.taskRunID, tt.setupTS)
			}

			payloadBytes, err := json.Marshal(tt.payload)
			require.NoError(t, err)

			resp, err := b.HandleCallback(payloadBytes)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, resp)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.taskRunID, resp.TaskRunID)
			assert.Equal(t, tt.wantApproved, resp.Approved)
			assert.Equal(t, tt.payload.User.ID, resp.Responder)

			// Verify pending request was removed after callback.
			_, ok := b.pendingRequests.Load(tt.taskRunID)
			assert.False(t, ok, "pending request should be removed after callback")
		})
	}
}

func TestSlackApprovalBackend_HandleCallback_InvalidJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := NewSlackApprovalBackend("C12345", "xoxb-test", logger)

	resp, err := b.HandleCallback([]byte("not valid json"))
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestSlackApprovalBackend_RequestAndCallback_Integration(t *testing.T) {
	// Test the full flow: request approval, then handle the callback.
	messageTS := "1234567890.123456"

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(slackResponse{OK: true, TS: messageTS})
		_, _ = w.Write(resp)
	})
	defer server.Close()

	b := newTestBackend(t, server.URL)

	// Step 1: Request approval.
	err := b.RequestApproval(
		context.Background(),
		"Should we deploy?",
		sampleTicket(),
		"run-integration",
		[]string{"approve", "reject"},
	)
	require.NoError(t, err)

	// Verify pending.
	ts, ok := b.pendingRequests.Load("run-integration")
	require.True(t, ok)
	assert.Equal(t, messageTS, ts)

	// Step 2: Simulate callback.
	callback := InteractionPayload{
		Type: "block_actions",
		Actions: []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		}{
			{ActionID: "robodev_approval_run-integration_0", Value: "approve"},
		},
		User: struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}{ID: "U99999", Username: "deployer"},
		Message: struct {
			TS string `json:"ts"`
		}{TS: messageTS},
	}
	callbackBytes, _ := json.Marshal(callback)

	resp, err := b.HandleCallback(callbackBytes)
	require.NoError(t, err)
	assert.Equal(t, "run-integration", resp.TaskRunID)
	assert.True(t, resp.Approved)
	assert.Equal(t, "U99999", resp.Responder)

	// Verify cleaned up.
	_, ok = b.pendingRequests.Load("run-integration")
	assert.False(t, ok)
}

func TestSlackApprovalBackend_RequestAndCancel_Integration(t *testing.T) {
	// Test the full flow: request approval, then cancel it.
	messageTS := "1234567890.123456"
	callCount := 0

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(slackResponse{OK: true, TS: messageTS})
		_, _ = w.Write(resp)
	})
	defer server.Close()

	b := newTestBackend(t, server.URL)

	// Step 1: Request approval.
	err := b.RequestApproval(
		context.Background(),
		"Should we deploy?",
		sampleTicket(),
		"run-cancel",
		[]string{"approve", "reject"},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "should have made one API call for request")

	// Step 2: Cancel.
	err = b.CancelPending(context.Background(), "run-cancel")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "should have made a second API call for cancel")

	// Verify cleaned up.
	_, ok := b.pendingRequests.Load("run-cancel")
	assert.False(t, ok)
}
