package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// scWebhookPayload is the subset of a Shortcut webhook delivery we parse.
type scWebhookPayload struct {
	Actions []scAction `json:"actions"`
}

// scAction represents a single action within a Shortcut webhook delivery.
type scAction struct {
	ID         int       `json:"id"`
	EntityType string    `json:"entity_type"`
	Action     string    `json:"action"`
	Name       string    `json:"name"`
	AppURL     string    `json:"app_url"`
	Changes    scChanges `json:"changes"`
}

// scChanges holds the changed fields on a Shortcut entity.
type scChanges struct {
	Description   *scChange              `json:"description,omitempty"`
	WorkflowState *scWorkflowStateChange `json:"workflow_state_id,omitempty"`
}

// scChange represents a simple old/new value change.
type scChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// scWorkflowStateChange represents a workflow state transition.
type scWorkflowStateChange struct {
	Old int `json:"old"`
	New int `json:"new"`
}

// handleShortcut processes incoming Shortcut webhook deliveries. It optionally
// validates the webhook signature if a secret is configured, then parses
// story update events into tickets.
func (s *Server) handleShortcut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate signature if a secret is configured.
	secret := s.secrets["shortcut"]
	if secret != "" {
		sigHeader := r.Header.Get("X-Shortcut-Signature")
		if !validateShortcutSignature(body, sigHeader, secret) {
			s.logger.Warn("invalid shortcut webhook signature")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var payload scWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Error("failed to parse shortcut webhook payload", slog.String("error", err.Error()))
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// Extract tickets from story_update actions.
	var tickets []ticketing.Ticket
	for _, action := range payload.Actions {
		if action.EntityType != "story" || action.Action != "update" {
			continue
		}

		ticket := ticketing.Ticket{
			ID:          strconv.Itoa(action.ID),
			Title:       action.Name,
			TicketType:  "story",
			ExternalURL: action.AppURL,
		}

		if action.Changes.Description != nil {
			ticket.Description = action.Changes.Description.New
		}

		tickets = append(tickets, ticket)
	}

	if len(tickets) == 0 {
		s.logger.Debug("no story updates in shortcut webhook")
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := s.handler.HandleWebhookEvent(r.Context(), "shortcut", tickets); err != nil {
		s.logger.Error("failed to handle shortcut webhook event", slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info("processed shortcut webhook",
		slog.Int("stories", len(tickets)),
	)
	w.WriteHeader(http.StatusOK)
}

// validateShortcutSignature checks the X-Shortcut-Signature header against
// the HMAC-SHA256 of the request body.
func validateShortcutSignature(body []byte, sigHeader, secret string) bool {
	if sigHeader == "" {
		return false
	}

	// Shortcut may prefix with "sha256=" or send the hex directly.
	sigHex := sigHeader
	if strings.HasPrefix(sigHex, "sha256=") {
		sigHex = sigHex[len("sha256="):]
	}

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// computeShortcutSignature computes the webhook signature value for testing.
func computeShortcutSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
