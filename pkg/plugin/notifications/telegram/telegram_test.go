package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// Compile-time interface check.
var _ notifications.Channel = (*TelegramChannel)(nil)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newTestChannel(t *testing.T, serverURL string) *TelegramChannel {
	t.Helper()
	return New("test-bot-token", "123456789", WithAPIURL(serverURL))
}

func sampleTicket() ticketing.Ticket {
	return ticketing.Ticket{
		ID:          "TICKET-42",
		Title:       "Fix broken login flow",
		Description: "Users cannot log in when using SSO",
		ExternalURL: "https://github.com/org/repo/issues/42",
	}
}

func TestTelegramChannel_Name(t *testing.T) {
	ch := New("tok", "chat")
	assert.Equal(t, "telegram", ch.Name())
}

func TestTelegramChannel_InterfaceVersion(t *testing.T) {
	ch := New("tok", "chat")
	assert.Equal(t, notifications.InterfaceVersion, ch.InterfaceVersion())
}

func TestTelegramChannel_Notify(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		ticket     ticketing.Ticket
		apiResp    telegramResponse
		statusCode int
		wantErr    bool
	}{
		{
			name:    "successful notification",
			message: "Agent is making progress",
			ticket:  sampleTicket(),
			apiResp: telegramResponse{OK: true},
		},
		{
			name:    "ticket without external URL",
			message: "Agent is making progress",
			ticket: ticketing.Ticket{
				ID:    "TICKET-99",
				Title: "Something",
			},
			apiResp: telegramResponse{OK: true},
		},
		{
			name:    "telegram API error",
			message: "Agent update",
			ticket:  sampleTicket(),
			apiResp: telegramResponse{OK: false, Description: "chat not found"},
			wantErr: true,
		},
		{
			name:       "HTTP error",
			message:    "Agent update",
			ticket:     sampleTicket(),
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

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Contains(t, r.URL.Path, "/bottest-bot-token/sendMessage")
				assert.Contains(t, r.Header.Get("Content-Type"), "application/json")

				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				var msg telegramMessage
				require.NoError(t, json.Unmarshal(body, &msg))
				assert.Equal(t, "123456789", msg.ChatID)
				assert.Equal(t, "Markdown", msg.ParseMode)
				assert.Contains(t, msg.Text, tt.message)

				if tt.ticket.ExternalURL != "" {
					assert.Contains(t, msg.Text, tt.ticket.ExternalURL)
				}

				w.WriteHeader(statusCode)
				resp, _ := json.Marshal(tt.apiResp)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.Notify(context.Background(), tt.message, tt.ticket, "")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTelegramChannel_NotifyStart(t *testing.T) {
	tests := []struct {
		name    string
		ticket  ticketing.Ticket
		wantErr bool
	}{
		{
			name:   "successful start notification",
			ticket: sampleTicket(),
		},
		{
			name: "ticket without external URL",
			ticket: ticketing.Ticket{
				ID:    "TICKET-99",
				Title: "Minimal ticket",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedMsg telegramMessage

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedMsg))

				resp, _ := json.Marshal(telegramResponse{OK: true})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			threadRef, err := ch.NotifyStart(context.Background(), tt.ticket)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Contains(t, capturedMsg.Text, tt.ticket.Title)
				assert.Contains(t, capturedMsg.Text, "started working on")
				// Telegram does not support threading; always returns empty ref.
				assert.Empty(t, threadRef)
			}

			if tt.ticket.ExternalURL != "" {
				assert.Contains(t, capturedMsg.Text, tt.ticket.ExternalURL)
			}
		})
	}
}

func TestTelegramChannel_NotifyComplete(t *testing.T) {
	tests := []struct {
		name            string
		ticket          ticketing.Ticket
		result          engine.TaskResult
		wantSuccess     bool
		wantMRLink      bool
		wantSummaryText string
	}{
		{
			name:   "successful completion with MR",
			ticket: sampleTicket(),
			result: engine.TaskResult{
				Success:         true,
				MergeRequestURL: "https://github.com/org/repo/pull/99",
				Summary:         "Fixed the login flow by updating SSO handler",
			},
			wantSuccess:     true,
			wantMRLink:      true,
			wantSummaryText: "Fixed the login flow",
		},
		{
			name:   "failed completion",
			ticket: sampleTicket(),
			result: engine.TaskResult{
				Success:  false,
				Summary:  "Could not reproduce the issue",
				ExitCode: 1,
			},
			wantSuccess: false,
		},
		{
			name:   "successful completion without MR",
			ticket: sampleTicket(),
			result: engine.TaskResult{
				Success: true,
				Summary: "Applied dependency upgrade",
			},
			wantSuccess: true,
			wantMRLink:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedMsg telegramMessage

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedMsg))

				resp, _ := json.Marshal(telegramResponse{OK: true})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.NotifyComplete(context.Background(), tt.ticket, tt.result, "")
			require.NoError(t, err)

			if tt.wantSuccess {
				assert.Contains(t, capturedMsg.Text, "succeeded")
			} else {
				assert.Contains(t, capturedMsg.Text, "failed")
			}

			if tt.wantMRLink {
				assert.Contains(t, capturedMsg.Text, tt.result.MergeRequestURL)
			}

			if tt.wantSummaryText != "" {
				assert.Contains(t, capturedMsg.Text, tt.wantSummaryText)
			}
		})
	}
}

func TestTelegramChannel_WithAPIURL(t *testing.T) {
	ch := New("tok", "chat")
	assert.Equal(t, defaultAPIURL, ch.apiURL)

	ch = New("tok", "chat", WithAPIURL("https://custom.telegram.api"))
	assert.Equal(t, "https://custom.telegram.api", ch.apiURL)
}

func TestTelegramChannel_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	ch := New("tok", "chat", WithHTTPClient(custom))
	assert.Equal(t, custom, ch.httpClient)
}

func TestTelegramChannel_WithThreadID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var msg telegramMessage
		require.NoError(t, json.Unmarshal(body, &msg))
		assert.Equal(t, 42, msg.MessageThreadID)

		resp, _ := json.Marshal(telegramResponse{OK: true})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})
	defer server.Close()

	ch := New("test-bot-token", "123456789", WithAPIURL(server.URL), WithThreadID(42))
	err := ch.Notify(context.Background(), "test", ticketing.Ticket{ID: "T-1", Title: "Test"}, "")
	assert.NoError(t, err)
}

func TestTelegramChannel_ThreadIDOmittedWhenZero(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		// Verify message_thread_id is not present in the JSON when threadID is 0.
		var raw map[string]any
		require.NoError(t, json.Unmarshal(body, &raw))
		_, hasThreadID := raw["message_thread_id"]
		assert.False(t, hasThreadID, "message_thread_id should be omitted when threadID is 0")

		resp, _ := json.Marshal(telegramResponse{OK: true})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})
	defer server.Close()

	ch := newTestChannel(t, server.URL)
	err := ch.Notify(context.Background(), "test", ticketing.Ticket{ID: "T-1", Title: "Test"}, "")
	assert.NoError(t, err)
}
