package discord

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
var _ notifications.Channel = (*DiscordChannel)(nil)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newTestChannel(t *testing.T, serverURL string) *DiscordChannel {
	t.Helper()
	return New(serverURL)
}

func sampleTicket() ticketing.Ticket {
	return ticketing.Ticket{
		ID:          "TICKET-42",
		Title:       "Fix broken login flow",
		Description: "Users cannot log in when using SSO",
		ExternalURL: "https://github.com/org/repo/issues/42",
	}
}

func TestDiscordChannel_Name(t *testing.T) {
	ch := New("https://discord.com/api/webhooks/test")
	assert.Equal(t, "discord", ch.Name())
}

func TestDiscordChannel_InterfaceVersion(t *testing.T) {
	ch := New("https://discord.com/api/webhooks/test")
	assert.Equal(t, notifications.InterfaceVersion, ch.InterfaceVersion())
}

func TestDiscordChannel_Notify(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		ticket     ticketing.Ticket
		statusCode int
		wantErr    bool
	}{
		{
			name:       "successful notification",
			message:    "Agent is making progress",
			ticket:     sampleTicket(),
			statusCode: http.StatusNoContent,
		},
		{
			name:    "ticket without external URL",
			message: "Agent is making progress",
			ticket: ticketing.Ticket{
				ID:    "TICKET-99",
				Title: "Something",
			},
			statusCode: http.StatusNoContent,
		},
		{
			name:       "discord API error",
			message:    "Agent update",
			ticket:     sampleTicket(),
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "server error",
			message:    "Agent update",
			ticket:     sampleTicket(),
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Contains(t, r.Header.Get("Content-Type"), "application/json")

				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				var payload discordWebhookPayload
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Len(t, payload.Embeds, 1)

				embed := payload.Embeds[0]
				assert.Equal(t, tt.message, embed.Description)
				assert.Equal(t, colourBlue, embed.Colour)

				if tt.ticket.ExternalURL != "" {
					assert.Equal(t, tt.ticket.ExternalURL, embed.URL)
				} else {
					assert.Empty(t, embed.URL)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode != http.StatusNoContent {
					_, _ = w.Write([]byte(`{"message":"error"}`))
				}
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

func TestDiscordChannel_NotifyStart(t *testing.T) {
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
			var capturedPayload discordWebhookPayload

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedPayload))

				w.WriteHeader(http.StatusNoContent)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			threadRef, err := ch.NotifyStart(context.Background(), tt.ticket)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				// Discord does not support threading; always returns empty ref.
				assert.Empty(t, threadRef)
				require.Len(t, capturedPayload.Embeds, 1)
				embed := capturedPayload.Embeds[0]
				assert.Equal(t, "Osmia Agent Started", embed.Title)
				assert.Equal(t, tt.ticket.Title, embed.Description)
				assert.Equal(t, colourBlue, embed.Colour)

				if tt.ticket.ExternalURL != "" {
					assert.Equal(t, tt.ticket.ExternalURL, embed.URL)
				} else {
					assert.Empty(t, embed.URL)
				}
			}
		})
	}
}

func TestDiscordChannel_NotifyComplete(t *testing.T) {
	tests := []struct {
		name            string
		ticket          ticketing.Ticket
		result          engine.TaskResult
		wantSuccess     bool
		wantMRLink      bool
		wantSummaryText string
		wantColour      int
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
			wantSummaryText: "Fixed the login flow by updating SSO handler",
			wantColour:      colourGreen,
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
			wantColour:  colourRed,
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
			wantColour:  colourGreen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedPayload discordWebhookPayload

			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &capturedPayload))

				w.WriteHeader(http.StatusNoContent)
			})
			defer server.Close()

			ch := newTestChannel(t, server.URL)
			err := ch.NotifyComplete(context.Background(), tt.ticket, tt.result, "")
			require.NoError(t, err)

			require.Len(t, capturedPayload.Embeds, 1)
			embed := capturedPayload.Embeds[0]

			if tt.wantSuccess {
				assert.Equal(t, "Task Succeeded", embed.Title)
			} else {
				assert.Equal(t, "Task Failed", embed.Title)
			}

			assert.Equal(t, tt.wantColour, embed.Colour)
			assert.Equal(t, tt.ticket.Title, embed.Description)

			if tt.ticket.ExternalURL != "" {
				assert.Equal(t, tt.ticket.ExternalURL, embed.URL)
			}

			if tt.wantMRLink {
				found := false
				for _, f := range embed.Fields {
					if f.Name == "Merge Request" {
						assert.Equal(t, tt.result.MergeRequestURL, f.Value)
						found = true
					}
				}
				assert.True(t, found, "expected Merge Request field in embed")
			}

			if tt.wantSummaryText != "" {
				found := false
				for _, f := range embed.Fields {
					if f.Name == "Summary" {
						assert.Equal(t, tt.wantSummaryText, f.Value)
						found = true
					}
				}
				assert.True(t, found, "expected Summary field in embed")
			}
		})
	}
}

func TestDiscordChannel_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	ch := New("https://discord.com/api/webhooks/test", WithHTTPClient(custom))
	assert.Equal(t, custom, ch.httpClient)
}

func TestDiscordChannel_WebhookURLUsage(t *testing.T) {
	webhookURL := ""

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		webhookURL = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	defer server.Close()

	ch := newTestChannel(t, server.URL+"/api/webhooks/12345/abcdef")
	err := ch.Notify(context.Background(), "test", ticketing.Ticket{ID: "T-1", Title: "Test"}, "")
	assert.NoError(t, err)
	assert.Equal(t, "/api/webhooks/12345/abcdef", webhookURL)
}
