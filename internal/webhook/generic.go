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
	"strings"

	svix "github.com/svix/svix-webhooks/go"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// GenericAuthMode defines the authentication method for the generic webhook.
type GenericAuthMode string

const (
	// GenericAuthHMAC validates requests using HMAC-SHA256 signature.
	GenericAuthHMAC GenericAuthMode = "hmac"

	// GenericAuthBearer validates requests using a bearer token.
	GenericAuthBearer GenericAuthMode = "bearer"

	// GenericAuthSvix validates requests using Svix-style signing
	// (used by incident.io, Stripe, OpenAI, Linear, and many other SaaS
	// providers built on Svix). Verification is delegated to the official
	// Svix Go library, which enforces a fixed five-minute timestamp
	// tolerance and accepts both "svix-*" and enterprise "webhook-*"
	// header prefixes.
	GenericAuthSvix GenericAuthMode = "svix"
)

// GenericConfig holds the configuration for the generic webhook handler.
type GenericConfig struct {
	// AuthMode is the authentication method: "hmac", "bearer", or "svix".
	AuthMode GenericAuthMode `json:"auth_mode" yaml:"auth_mode"`

	// Secret is the HMAC secret, Svix signing key, or bearer token,
	// depending on AuthMode. For Svix mode, a "whsec_" prefix is
	// recognised and the remainder is base64-decoded before use.
	Secret string `json:"secret" yaml:"secret"`

	// SignatureHeader is the header containing the HMAC signature.
	// Defaults to "X-Webhook-Signature" if empty. Only used in HMAC mode;
	// Svix mode uses fixed headers (svix-signature or webhook-signature).
	SignatureHeader string `json:"signature_header" yaml:"signature_header"`

	// FieldMapping maps dot-notation JSON paths to ticket fields.
	// Supported target fields: id, title, description, ticket_type, repo_url, external_url.
	FieldMapping map[string]string `json:"field_mapping" yaml:"field_mapping"`
}

// handleGeneric processes incoming generic webhook deliveries. It supports
// configurable authentication (HMAC or bearer token) and configurable JSON
// field mapping for extracting ticket data from arbitrary payloads.
func (s *Server) handleGeneric(w http.ResponseWriter, r *http.Request) {
	if s.genericConfig == nil {
		s.logger.Error("generic webhook not configured")
		http.Error(w, "generic webhook not configured", http.StatusInternalServerError)
		return
	}

	cfg := s.genericConfig

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate authentication.
	switch cfg.AuthMode {
	case GenericAuthHMAC:
		sigHeader := cfg.SignatureHeader
		if sigHeader == "" {
			sigHeader = "X-Webhook-Signature"
		}
		sig := r.Header.Get(sigHeader)
		if !validateGenericHMACSignature(body, sig, cfg.Secret) {
			s.logger.Warn("invalid generic webhook hmac signature")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	case GenericAuthBearer:
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + cfg.Secret
		if auth != expected {
			s.logger.Warn("invalid generic webhook bearer token")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	case GenericAuthSvix:
		wh, err := svix.NewWebhook(cfg.Secret)
		if err != nil {
			s.logger.Error("invalid svix secret", slog.String("error", err.Error()))
			http.Error(w, "invalid auth configuration", http.StatusInternalServerError)
			return
		}
		if err := wh.Verify(body, r.Header); err != nil {
			s.logger.Warn("invalid generic webhook svix signature", slog.String("error", err.Error()))
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	default:
		s.logger.Error("unknown generic webhook auth mode", slog.String("mode", string(cfg.AuthMode)))
		http.Error(w, "invalid auth configuration", http.StatusInternalServerError)
		return
	}

	// Parse JSON payload.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		s.logger.Error("failed to parse generic webhook payload", slog.String("error", err.Error()))
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// Extract ticket fields using configured field mapping.
	ticket := ticketing.Ticket{}
	if cfg.FieldMapping != nil {
		for jsonPath, field := range cfg.FieldMapping {
			val := extractJSONPath(raw, jsonPath)
			if val == "" {
				continue
			}
			switch field {
			case "id":
				ticket.ID = val
			case "title":
				ticket.Title = val
			case "description":
				ticket.Description = val
			case "ticket_type":
				ticket.TicketType = val
			case "repo_url":
				ticket.RepoURL = val
			case "external_url":
				ticket.ExternalURL = val
			}
		}
	}

	if ticket.ID == "" {
		s.logger.Warn("generic webhook payload did not produce a ticket ID")
		http.Error(w, "missing ticket id in payload", http.StatusBadRequest)
		return
	}

	if err := s.handler.HandleWebhookEvent(r.Context(), "generic", []ticketing.Ticket{ticket}); err != nil {
		s.logger.Error("failed to handle generic webhook event", slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info("processed generic webhook",
		slog.String("ticket_id", ticket.ID),
		slog.String("title", ticket.Title),
	)
	w.WriteHeader(http.StatusOK)
}

// extractJSONPath extracts a value from a nested map using simple dot-notation
// paths (e.g. "issue.title"). This is intentionally simple — it does not
// support array indexing or complex JSONPath expressions.
func extractJSONPath(data map[string]any, path string) string {
	parts := strings.Split(path, ".")
	var current any = data

	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[part]
	}

	if current == nil {
		return ""
	}

	switch v := current.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// validateGenericHMACSignature checks the signature header against the
// HMAC-SHA256 of the request body. The signature may be hex-encoded with
// or without a "sha256=" prefix.
func validateGenericHMACSignature(body []byte, sigHeader, secret string) bool {
	if sigHeader == "" {
		return false
	}

	sigHex := strings.TrimPrefix(sigHeader, "sha256=")

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// computeGenericHMACSignature computes the HMAC-SHA256 signature for testing.
func computeGenericHMACSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
