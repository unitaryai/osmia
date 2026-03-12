package webhook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// mockEventHandler records calls to HandleWebhookEvent for testing.
type mockEventHandler struct {
	calls []mockHandlerCall
	err   error
}

type mockHandlerCall struct {
	source  string
	tickets []ticketing.Ticket
}

func (m *mockEventHandler) HandleWebhookEvent(_ context.Context, source string, tickets []ticketing.Ticket) error {
	m.calls = append(m.calls, mockHandlerCall{source: source, tickets: tickets})
	return m.err
}

func TestWebhookEvent_Fields(t *testing.T) {
	event := WebhookEvent{
		Source:     "github",
		Tickets:    []ticketing.Ticket{{ID: "1", Title: "Test"}},
		RawPayload: []byte(`{"test": true}`),
	}

	assert.Equal(t, "github", event.Source)
	assert.Len(t, event.Tickets, 1)
	assert.Equal(t, "1", event.Tickets[0].ID)
	assert.JSONEq(t, `{"test": true}`, string(event.RawPayload))
}
