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

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// ghWebhookPayload is the subset of a GitHub webhook delivery we parse.
type ghWebhookPayload struct {
	Action string  `json:"action"`
	Issue  ghIssue `json:"issue"`
	Repo   ghRepo  `json:"repository"`
}

// ghIssue is the subset of a GitHub issue in a webhook payload.
type ghIssue struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	HTMLURL string    `json:"html_url"`
	Labels  []ghLabel `json:"labels"`
}

// ghLabel is a GitHub issue label.
type ghLabel struct {
	Name string `json:"name"`
}

// ghRepo is the subset of a GitHub repository in a webhook payload.
type ghRepo struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
}

// handleGitHub processes incoming GitHub webhook deliveries. It validates the
// HMAC-SHA256 signature before parsing the payload to prevent processing
// tampered or unauthorised requests.
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	secret := s.secrets["github"]
	if secret == "" {
		s.logger.Error("github webhook secret not configured")
		http.Error(w, "webhook secret not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate signature before any JSON parsing (fail-fast).
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !validateGitHubSignature(body, sigHeader, secret) {
		s.logger.Warn("invalid github webhook signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Only process issue events.
	eventType := r.Header.Get("X-GitHub-Event")
	if eventType != "issues" {
		s.logger.Debug("ignoring non-issue github event", slog.String("event", eventType))
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload ghWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Error("failed to parse github webhook payload", slog.String("error", err.Error()))
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// Only process opened or labelled actions.
	if payload.Action != "opened" && payload.Action != "labeled" {
		s.logger.Debug("ignoring github issue action", slog.String("action", payload.Action))
		w.WriteHeader(http.StatusOK)
		return
	}

	labels := make([]string, 0, len(payload.Issue.Labels))
	for _, l := range payload.Issue.Labels {
		labels = append(labels, l.Name)
	}

	// If trigger labels are configured, only forward issues that carry at least
	// one of them. This mirrors the polling backend's label-gating behaviour.
	if len(s.githubTriggerLabels) > 0 {
		found := false
		for _, want := range s.githubTriggerLabels {
			for _, got := range labels {
				if got == want {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			s.logger.Debug("ignoring github issue: no trigger label",
				slog.Int("issue", payload.Issue.Number),
				slog.String("action", payload.Action),
			)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	ticket := ticketing.Ticket{
		ID:          strconv.Itoa(payload.Issue.Number),
		Title:       payload.Issue.Title,
		Description: payload.Issue.Body,
		TicketType:  "issue",
		Labels:      labels,
		RepoURL:     payload.Repo.HTMLURL,
		ExternalURL: payload.Issue.HTMLURL,
	}

	if err := s.handler.HandleWebhookEvent(r.Context(), "github", []ticketing.Ticket{ticket}); err != nil {
		s.logger.Error("failed to handle github webhook event", slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info("processed github webhook",
		slog.String("action", payload.Action),
		slog.String("issue", strconv.Itoa(payload.Issue.Number)),
		slog.String("repo", payload.Repo.FullName),
	)
	w.WriteHeader(http.StatusOK)
}

// validateGitHubSignature checks the X-Hub-Signature-256 header against
// the HMAC-SHA256 of the request body using the configured secret.
func validateGitHubSignature(body []byte, sigHeader, secret string) bool {
	if sigHeader == "" {
		return false
	}

	prefix := "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}

	sigHex := sigHeader[len(prefix):]
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// computeGitHubSignature computes the X-Hub-Signature-256 value for testing.
func computeGitHubSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
