package webhook

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewServer_RoutesRegistered(t *testing.T) {
	handler := &mockEventHandler{}
	srv := NewServer(testLogger(), handler,
		WithSecret("github", "test-secret"),
		WithSecret("gitlab", "test-secret"),
		WithSecret("slack", "test-secret"),
	)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{
			name:   "healthz returns 200",
			method: http.MethodGet,
			path:   "/healthz",
			want:   http.StatusOK,
		},
		{
			name:   "unknown path returns 404",
			method: http.MethodPost,
			path:   "/webhooks/unknown",
			want:   http.StatusNotFound,
		},
		{
			name:   "GET on webhook path returns 405",
			method: http.MethodGet,
			path:   "/webhooks/github",
			want:   http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestServer_RegisterRoute(t *testing.T) {
	handler := &mockEventHandler{}
	srv := NewServer(testLogger(), handler)

	srv.RegisterRoute("GET /custom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodGet, "/custom", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestServer_Healthz(t *testing.T) {
	handler := &mockEventHandler{}
	srv := NewServer(testLogger(), handler)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestServer_ShutdownNilServer(t *testing.T) {
	handler := &mockEventHandler{}
	srv := NewServer(testLogger(), handler)

	// Shutdown before ListenAndServe should not panic.
	err := srv.Shutdown(t.Context())
	assert.NoError(t, err)
}
