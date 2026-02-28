package webhook

import (
	"context"
	"log/slog"
	"net"
	"net/http"
)

// Server is the HTTP webhook receiver. It registers route handlers for each
// supported webhook source and delegates parsed events to an EventHandler.
type Server struct {
	mux     *http.ServeMux
	handler EventHandler
	logger  *slog.Logger
	server  *http.Server

	// secrets holds per-source webhook secrets used for signature validation.
	secrets map[string]string

	// genericConfig holds the configuration for the generic webhook handler.
	genericConfig *GenericConfig
}

// Option is a functional option for configuring a Server.
type Option func(*Server)

// WithSecret configures a webhook signing secret for the given source.
// Supported sources: "github", "gitlab", "slack", "shortcut".
func WithSecret(source, secret string) Option {
	return func(s *Server) {
		s.secrets[source] = secret
	}
}

// WithGenericConfig sets the configuration for the generic webhook handler.
func WithGenericConfig(cfg *GenericConfig) Option {
	return func(s *Server) {
		s.genericConfig = cfg
	}
}

// NewServer creates a new webhook Server with routes registered for each
// supported source. The handler receives parsed webhook events. Use
// functional options to configure per-source secrets.
func NewServer(logger *slog.Logger, handler EventHandler, opts ...Option) *Server {
	s := &Server{
		mux:     http.NewServeMux(),
		handler: handler,
		logger:  logger,
		secrets: make(map[string]string),
	}

	for _, opt := range opts {
		opt(s)
	}

	// Register routes.
	s.mux.HandleFunc("POST /webhooks/github", s.handleGitHub)
	s.mux.HandleFunc("POST /webhooks/gitlab", s.handleGitLab)
	s.mux.HandleFunc("POST /webhooks/slack", s.handleSlack)
	s.mux.HandleFunc("POST /webhooks/shortcut", s.handleShortcut)
	s.mux.HandleFunc("POST /webhooks/generic", s.handleGeneric)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	return s
}

// RegisterRoute adds a custom route to the server's mux. This can be used
// to extend the server with additional webhook sources beyond the built-in
// handlers.
func (s *Server) RegisterRoute(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	s.server = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}
	s.logger.Info("webhook server starting", slog.String("addr", addr))
	return s.server.ListenAndServe()
}

// Serve accepts connections on the given listener.
func (s *Server) Serve(ln net.Listener) error {
	s.server = &http.Server{
		Handler: s.mux,
	}
	s.logger.Info("webhook server starting", slog.String("addr", ln.Addr().String()))
	return s.server.Serve(ln)
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	s.logger.Info("webhook server shutting down")
	return s.server.Shutdown(ctx)
}

// ServeHTTP implements http.Handler, allowing the Server to be used directly
// in tests or composed into a larger mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleHealthz responds with 200 OK for liveness/readiness probes.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
