package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const (
	// slackTimestampMaxAge is the maximum age of a Slack request timestamp
	// before it is considered a replay attack.
	slackTimestampMaxAge = 5 * time.Minute
)

// slackInteractionPayload is the subset of a Slack interaction callback we
// parse. Slash commands and interactive messages both arrive as form-encoded
// payloads with a "payload" field containing JSON.
type slackInteractionPayload struct {
	Type    string `json:"type"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	TriggerID string `json:"trigger_id"`
	// Command is populated for slash commands.
	Command string `json:"command"`
	Text    string `json:"text"`
}

// handleSlack processes incoming Slack webhook deliveries. It validates the
// X-Slack-Signature using HMAC-SHA256 with the v0:timestamp:body format and
// checks that the timestamp is within 5 minutes to prevent replay attacks.
func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
	secret := s.secrets["slack"]
	if secret == "" {
		s.logger.Error("slack webhook secret not configured")
		http.Error(w, "webhook secret not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate timestamp freshness (replay attack prevention).
	tsHeader := r.Header.Get("X-Slack-Request-Timestamp")
	if tsHeader == "" {
		s.logger.Warn("missing slack request timestamp")
		http.Error(w, "missing timestamp", http.StatusUnauthorized)
		return
	}

	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		s.logger.Warn("invalid slack request timestamp", slog.String("timestamp", tsHeader))
		http.Error(w, "invalid timestamp", http.StatusUnauthorized)
		return
	}

	if math.Abs(float64(time.Now().Unix()-ts)) > slackTimestampMaxAge.Seconds() {
		s.logger.Warn("slack request timestamp too old", slog.String("timestamp", tsHeader))
		http.Error(w, "request too old", http.StatusUnauthorized)
		return
	}

	// Validate HMAC-SHA256 signature.
	sigHeader := r.Header.Get("X-Slack-Signature")
	if !validateSlackSignature(body, tsHeader, sigHeader, secret) {
		s.logger.Warn("invalid slack webhook signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Slack interaction payloads may arrive as form-encoded with a "payload"
	// field, or as raw JSON. Try form-encoded first.
	var payloadBytes []byte
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		// Re-parse form data from the body we already read.
		payloadStr := extractFormValue(body, "payload")
		if payloadStr == "" {
			s.logger.Warn("missing payload field in slack form data")
			http.Error(w, "missing payload", http.StatusBadRequest)
			return
		}
		payloadBytes = []byte(payloadStr)
	} else {
		payloadBytes = body
	}

	var payload slackInteractionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		s.logger.Error("failed to parse slack payload", slog.String("error", err.Error()))
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// Separate approval/rejection callbacks from other interactions.
	// Approval callbacks use action IDs like robodev_approval_{taskRunID}_{i}.
	// These must NOT be forwarded to ProcessTicket — doing so would create a
	// spurious task run. Instead they are routed to the ApprovalHandler when
	// configured, or acknowledged and logged otherwise.
	var nonApprovalActions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	}
	for _, action := range payload.Actions {
		if strings.HasPrefix(action.ActionID, "robodev_approval_") {
			// Extract taskRunID: split on "_" and rejoin segments [2..len-1].
			parts := strings.Split(action.ActionID, "_")
			var taskRunID string
			if len(parts) > 3 {
				taskRunID = strings.Join(parts[2:len(parts)-1], "_")
			} else if len(parts) == 3 {
				taskRunID = parts[2]
			}

			approved := action.Value != "reject" && action.Value != "deny"

			if s.approvalHandler != nil {
				if err := s.approvalHandler.HandleApprovalCallback(r.Context(), taskRunID, approved, payload.User.Username); err != nil {
					s.logger.Error("approval handler failed",
						slog.String("task_run_id", taskRunID),
						slog.String("error", err.Error()),
					)
					// Return 500 so Slack retries the delivery rather than
					// silently losing the approval callback.
					http.Error(w, "approval handler error", http.StatusInternalServerError)
					return
				}
			} else {
				s.logger.Info("received approval callback but no handler configured",
					slog.String("task_run_id", taskRunID),
					slog.Bool("approved", approved),
					slog.String("user", payload.User.Username),
				)
			}
		} else {
			nonApprovalActions = append(nonApprovalActions, action)
		}
	}

	// Only forward non-approval Slack interactions (e.g. slash commands,
	// other button actions) to the event handler.
	if len(nonApprovalActions) > 0 {
		tickets := make([]ticketing.Ticket, 0, len(nonApprovalActions))
		for _, action := range nonApprovalActions {
			tickets = append(tickets, ticketing.Ticket{
				ID:         action.ActionID,
				Title:      action.Value,
				TicketType: "slack_interaction",
			})
		}
		if err := s.handler.HandleWebhookEvent(r.Context(), "slack", tickets); err != nil {
			s.logger.Error("failed to handle slack webhook event", slog.String("error", err.Error()))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	s.logger.Info("processed slack webhook",
		slog.String("type", payload.Type),
		slog.Int("actions", len(payload.Actions)),
	)
	w.WriteHeader(http.StatusOK)
}

// validateSlackSignature verifies the X-Slack-Signature header using the
// v0:timestamp:body signing scheme.
func validateSlackSignature(body []byte, timestamp, sigHeader, secret string) bool {
	if sigHeader == "" {
		return false
	}

	prefix := "v0="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}

	sigHex := sigHeader[len(prefix):]
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// computeSlackSignature computes the X-Slack-Signature value for testing.
func computeSlackSignature(body []byte, timestamp, secret string) string {
	baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	return fmt.Sprintf("v0=%s", hex.EncodeToString(mac.Sum(nil)))
}

// extractFormValue extracts a value from a URL-encoded form body without
// relying on http.Request.ParseForm (since we've already consumed the body).
func extractFormValue(body []byte, key string) string {
	parts := strings.Split(string(body), "&")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 && kv[0] == key {
			// URL-decode the value. Simple replacement of + with space
			// and percent-decoding. For robustness in production you'd
			// use url.QueryUnescape, but we keep it simple here.
			val := kv[1]
			val = strings.ReplaceAll(val, "+", " ")
			decoded, err := decodePercent(val)
			if err != nil {
				return val
			}
			return decoded
		}
	}
	return ""
}

// decodePercent performs percent-decoding on a string.
func decodePercent(s string) (string, error) {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			high, err := hex.DecodeString(s[i+1 : i+3])
			if err != nil {
				return "", err
			}
			buf.WriteByte(high[0])
			i += 2
		} else {
			buf.WriteByte(s[i])
		}
	}
	return buf.String(), nil
}
