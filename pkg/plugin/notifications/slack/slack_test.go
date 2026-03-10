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

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newTestChannel(t *testing.T, serverURL string) *SlackChannel {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewSlackChannel("C12345", "xoxb-test-token", logger).
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

func TestSlackChannel_ImplementsInterface(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var ch notifications.Channel = NewSlackChannel("C12345", "xoxb-test", logger)
	assert.NotNil(t, ch)
}

func TestSlackChannel_Name(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ch := NewSlackChannel("C12345", "xoxb-test", logger)
	assert.Equal(t, "slack", ch.Name())
}

func TestSlackChannel_InterfaceVersion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ch := NewSlackChannel("C12345", "xoxb-test", logger)
	assert.Equal(t, notifications.InterfaceVersion, ch.InterfaceVersion())
}

func TestSlackChannel_Notify(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		ticket     ticketing.Ticket
		apiResp    slackResponse
		statusCode int
		wantErr    bool
	}{
		{
			name:    "successful notification",
			message: "Agent is making progress",
			ticket:  sampleTicket(),
			apiResp: slackResponse{OK: true, TS: "1234567890.123456"},
		},
		{
			name:    "ticket without external URL",
			message: "Agent is making progress",
			ticket: ticketing.Ticket{
				ID:    "TICKET-99",
				Title: "Something",
			},
			apiResp: slackResponse{OK: true, TS: "1234567890.123456"},
		},
		{
			name:    "slack API error",
			message: "Agent update",
			ticket:  sampleTicket(),
			apiResp: slackResponse{OK: false, Error: "channel_not_found"},
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
				// Verify request properties.
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/chat.postMessage", r.URL.Path)
				assert.Equal(t, "Bearer xoxb-test-token", r.Header.Get("Authorization"))
				assert.Contains(t, r.Header.Get("Content-Type"), "application/json")

				// Verify request body.
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				var msg slackMessage
				require.NoError(t, json.Unmarshal(body, &msg))
				assert.Equal(t, "C12345", msg.Channel)
				assert.NotEmpty(t, msg.Blocks)

				w.WriteHeader(statusCode)
				resp, _ := json.Marshal(tt.apiResp)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.Notify(context.Background(), tt.message, tt.ticket)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSlackChannel_NotifyStart(t *testing.T) {
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
			var capturedMsg slackMessage

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedMsg))

				resp, _ := json.Marshal(slackResponse{OK: true, TS: "1234567890.123456"})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.NotifyStart(context.Background(), tt.ticket)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Contains(t, capturedMsg.Text, tt.ticket.Title)
				assert.Contains(t, capturedMsg.Text, "started working on")
			}
		})
	}
}

func TestSlackChannel_NotifyComplete(t *testing.T) {
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
			var capturedMsg slackMessage

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedMsg))

				resp, _ := json.Marshal(slackResponse{OK: true, TS: "1234567890.123456"})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(resp)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.NotifyComplete(context.Background(), tt.ticket, tt.result)
			require.NoError(t, err)

			if tt.wantSuccess {
				assert.Contains(t, capturedMsg.Text, "succeeded")
			} else {
				assert.Contains(t, capturedMsg.Text, "failed")
			}

			// Check blocks contain MR link when expected.
			blocksJSON, _ := json.Marshal(capturedMsg.Blocks)
			blocksStr := string(blocksJSON)
			if tt.wantMRLink {
				assert.Contains(t, blocksStr, tt.result.MergeRequestURL)
			}
		})
	}
}

func TestSlackChannel_WithAPIURL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ch := NewSlackChannel("C12345", "xoxb-test", logger)
	assert.Equal(t, defaultAPIURL, ch.apiURL)

	ch.WithAPIURL("https://custom.slack.api")
	assert.Equal(t, "https://custom.slack.api", ch.apiURL)
}
