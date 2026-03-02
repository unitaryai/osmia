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

func TestHandleShortcut(t *testing.T) {
	secret := "test-shortcut-secret"

	storyUpdatePayload := scWebhookPayload{
		Actions: []scAction{
			{
				ID:         12345,
				EntityType: "story",
				Action:     "update",
				Name:       "Fix authentication flow",
				AppURL:     "https://app.shortcut.com/workspace/story/12345",
				Changes: scChanges{
					Description: &scChange{
						Old: "Old description",
						New: "Updated description of the fix",
					},
				},
			},
		},
	}

	tests := []struct {
		name       string
		payload    any
		secret     string
		sigFunc    func([]byte) string
		wantStatus int
		wantCalls  int
	}{
		{
			name:    "valid story update with signature",
			payload: storyUpdatePayload,
			secret:  secret,
			sigFunc: func(b []byte) string {
				return computeShortcutSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "valid story update without secret (no validation)",
			payload:    storyUpdatePayload,
			secret:     "", // no secret configured, so signature is not validated
			sigFunc:    func(_ []byte) string { return "" },
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "invalid signature",
			payload:    storyUpdatePayload,
			secret:     secret,
			sigFunc:    func(_ []byte) string { return "sha256=deadbeef" },
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name: "non-story entity ignored",
			payload: scWebhookPayload{
				Actions: []scAction{
					{
						ID:         1,
						EntityType: "epic",
						Action:     "update",
						Name:       "Epic update",
					},
				},
			},
			secret:     "",
			sigFunc:    func(_ []byte) string { return "" },
			wantStatus: http.StatusOK,
			wantCalls:  0,
		},
		{
			name: "non-update action ignored",
			payload: scWebhookPayload{
				Actions: []scAction{
					{
						ID:         1,
						EntityType: "story",
						Action:     "create",
						Name:       "New story",
					},
				},
			},
			secret:     "",
			sigFunc:    func(_ []byte) string { return "" },
			wantStatus: http.StatusOK,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}

			opts := []Option{}
			if tc.secret != "" {
				opts = append(opts, WithSecret("shortcut", tc.secret))
			}
			srv := NewServer(testLogger(), mock, opts...)

			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			sig := tc.sigFunc(body)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if sig != "" {
				req.Header.Set("X-Shortcut-Signature", sig)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				call := mock.calls[0]
				assert.Equal(t, "shortcut", call.source)
				require.Len(t, call.tickets, 1)
				assert.Equal(t, "12345", call.tickets[0].ID)
				assert.Equal(t, "Fix authentication flow", call.tickets[0].Title)
				assert.Equal(t, "Updated description of the fix", call.tickets[0].Description)
				assert.Equal(t, "story", call.tickets[0].TicketType)
				assert.Equal(t, "https://app.shortcut.com/workspace/story/12345", call.tickets[0].ExternalURL)
			}
		})
	}
}

func TestHandleShortcut_MultipleActions(t *testing.T) {
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock)

	payload := scWebhookPayload{
		Actions: []scAction{
			{
				ID:         100,
				EntityType: "story",
				Action:     "update",
				Name:       "First story",
				AppURL:     "https://app.shortcut.com/workspace/story/100",
			},
			{
				ID:         200,
				EntityType: "story",
				Action:     "update",
				Name:       "Second story",
				AppURL:     "https://app.shortcut.com/workspace/story/200",
			},
			{
				ID:         300,
				EntityType: "epic",
				Action:     "update",
				Name:       "An epic (should be ignored)",
			},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, mock.calls, 1)
	assert.Len(t, mock.calls[0].tickets, 2)
	assert.Equal(t, "100", mock.calls[0].tickets[0].ID)
	assert.Equal(t, "200", mock.calls[0].tickets[1].ID)
}

func TestHandleShortcut_MalformedJSON(t *testing.T) {
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleShortcut_HandlerError(t *testing.T) {
	mock := &mockEventHandler{err: fmt.Errorf("handler failed")}
	srv := NewServer(testLogger(), mock)

	payload := scWebhookPayload{
		Actions: []scAction{
			{
				ID:         1,
				EntityType: "story",
				Action:     "update",
				Name:       "Test",
				AppURL:     "https://app.shortcut.com/workspace/story/1",
			},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- State transition filtering ---

func TestHandleShortcut_TargetStateFilter(t *testing.T) {
	const targetState = int64(200)

	tests := []struct {
		name      string
		action    scAction
		wantCalls int
	}{
		{
			name: "workflow state transitions to target — forwarded",
			action: scAction{
				ID:         1,
				EntityType: "story",
				Action:     "update",
				Name:       "Ready story",
				AppURL:     "https://app.shortcut.com/workspace/story/1",
				Changes: scChanges{
					WorkflowState: &scWorkflowStateChange{Old: 100, New: 200},
				},
			},
			wantCalls: 1,
		},
		{
			name: "workflow state transitions to a different state — ignored",
			action: scAction{
				ID:         2,
				EntityType: "story",
				Action:     "update",
				Name:       "Wrong state story",
				AppURL:     "https://app.shortcut.com/workspace/story/2",
				Changes: scChanges{
					WorkflowState: &scWorkflowStateChange{Old: 100, New: 300},
				},
			},
			wantCalls: 0,
		},
		{
			name: "description change only (no state change) — ignored",
			action: scAction{
				ID:         3,
				EntityType: "story",
				Action:     "update",
				Name:       "Description update",
				AppURL:     "https://app.shortcut.com/workspace/story/3",
				Changes: scChanges{
					Description: &scChange{Old: "old", New: "new"},
				},
			},
			wantCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			srv := NewServer(testLogger(), mock, WithShortcutTargetStateID(targetState))

			payload := scWebhookPayload{Actions: []scAction{tc.action}}
			body, _ := json.Marshal(payload)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)
		})
	}
}

func TestHandleShortcut_NoTargetStateFilter_AllUpdatesForwarded(t *testing.T) {
	// Without a target state ID, all story updates pass through (existing behaviour).
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock) // no WithShortcutTargetStateID

	payload := scWebhookPayload{
		Actions: []scAction{
			{
				ID:         42,
				EntityType: "story",
				Action:     "update",
				Name:       "Any story update",
				AppURL:     "https://app.shortcut.com/workspace/story/42",
				Changes: scChanges{
					Description: &scChange{Old: "old", New: "new"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/shortcut", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, mock.calls, 1)
}

func TestWithShortcutTargetStateID(t *testing.T) {
	srv := NewServer(testLogger(), &mockEventHandler{}, WithShortcutTargetStateID(500))
	assert.Equal(t, int64(500), srv.shortcutTargetStateID)
}

func TestValidateShortcutSignature(t *testing.T) {
	body := []byte(`{"test": true}`)
	secret := "my-secret"

	tests := []struct {
		name string
		sig  string
		want bool
	}{
		{
			name: "valid signature with prefix",
			sig:  computeShortcutSignature(body, secret),
			want: true,
		},
		{
			name: "valid signature without prefix",
			sig: func() string {
				full := computeShortcutSignature(body, secret)
				return full[len("sha256="):]
			}(),
			want: true,
		},
		{
			name: "empty signature",
			sig:  "",
			want: false,
		},
		{
			name: "invalid hex",
			sig:  "sha256=zzzz",
			want: false,
		},
		{
			name: "wrong secret",
			sig:  computeShortcutSignature(body, "wrong-secret"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateShortcutSignature(body, tc.sig, secret)
			assert.Equal(t, tc.want, got)
		})
	}
}
