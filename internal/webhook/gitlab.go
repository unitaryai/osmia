package webhook

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// glWebhookPayload is the subset of a GitLab webhook delivery we parse.
// GitLab uses "object_kind" to identify the event type.
type glWebhookPayload struct {
	ObjectKind       string             `json:"object_kind"`
	ObjectAttributes glObjectAttributes `json:"object_attributes"`
	Project          glProject          `json:"project"`
	Labels           []glLabel          `json:"labels"`
}

// glObjectAttributes holds the attributes of the issue or merge request.
type glObjectAttributes struct {
	IID    int    `json:"iid"`
	Title  string `json:"title"`
	Desc   string `json:"description"`
	URL    string `json:"url"`
	Action string `json:"action"`
	State  string `json:"state"`
}

// glProject is the subset of a GitLab project in a webhook payload.
type glProject struct {
	WebURL string `json:"web_url"`
	PathNS string `json:"path_with_namespace"`
}

// glLabel is a GitLab label.
type glLabel struct {
	Title string `json:"title"`
}

// handleGitLab processes incoming GitLab webhook deliveries. It validates the
// X-Gitlab-Token header against the configured secret before parsing.
func (s *Server) handleGitLab(w http.ResponseWriter, r *http.Request) {
	secret := s.secrets["gitlab"]
	if secret == "" {
		s.logger.Error("gitlab webhook secret not configured")
		http.Error(w, "webhook secret not configured", http.StatusInternalServerError)
		return
	}

	// Validate token before reading body (fail-fast).
	token := r.Header.Get("X-Gitlab-Token")
	if token != secret {
		s.logger.Warn("invalid gitlab webhook token")
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var payload glWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Error("failed to parse gitlab webhook payload", slog.String("error", err.Error()))
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// Only process issue and merge request events.
	var ticketType string
	switch payload.ObjectKind {
	case "issue":
		ticketType = "issue"
	case "merge_request":
		ticketType = "merge_request"
	default:
		s.logger.Debug("ignoring gitlab event", slog.String("object_kind", payload.ObjectKind))
		w.WriteHeader(http.StatusOK)
		return
	}

	labels := make([]string, 0, len(payload.Labels))
	for _, l := range payload.Labels {
		labels = append(labels, l.Title)
	}

	ticket := ticketing.Ticket{
		ID:          strconv.Itoa(payload.ObjectAttributes.IID),
		Title:       payload.ObjectAttributes.Title,
		Description: payload.ObjectAttributes.Desc,
		TicketType:  ticketType,
		Labels:      labels,
		RepoURL:     payload.Project.WebURL,
		ExternalURL: payload.ObjectAttributes.URL,
	}

	if err := s.handler.HandleWebhookEvent(r.Context(), "gitlab", []ticketing.Ticket{ticket}); err != nil {
		s.logger.Error("failed to handle gitlab webhook event", slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info("processed gitlab webhook",
		slog.String("object_kind", payload.ObjectKind),
		slog.String("iid", strconv.Itoa(payload.ObjectAttributes.IID)),
		slog.String("project", payload.Project.PathNS),
	)
	w.WriteHeader(http.StatusOK)
}
