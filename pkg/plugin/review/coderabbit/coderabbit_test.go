package coderabbit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/pkg/plugin/review"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCodeRabbitBackend_Name(t *testing.T) {
	b := NewCodeRabbitBackend("key", testLogger())
	assert.Equal(t, "coderabbit", b.Name())
}

func TestCodeRabbitBackend_InterfaceVersion(t *testing.T) {
	b := NewCodeRabbitBackend("key", testLogger())
	assert.Equal(t, review.InterfaceVersion, b.InterfaceVersion())
}

func TestCodeRabbitBackend_ReviewDiff_PassThrough(t *testing.T) {
	b := NewCodeRabbitBackend("", testLogger())
	result, err := b.ReviewDiff(context.Background(), "task-123", "some diff")
	require.NoError(t, err)

	assert.True(t, result.Passed)
	assert.Contains(t, result.Summary, "pass-through")
	assert.Empty(t, result.Comments)
	assert.Empty(t, result.SecurityFindings)
}

func TestCodeRabbitBackend_ReviewDiff_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/reviews", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req crReviewRequest
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "task-456", req.TaskRunID)
		assert.Equal(t, "diff content here", req.Diff)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crReviewResponse{
			Passed:  true,
			Summary: "All checks passed",
			Comments: []crComment{
				{
					FilePath: "main.go",
					Line:     10,
					Body:     "Consider adding a comment here",
					Severity: "info",
				},
			},
			Security: []crSecurityIssue{
				{
					RuleID:      "SEC-001",
					Description: "Potential SQL injection",
					FilePath:    "db.go",
					Line:        42,
					Severity:    "high",
				},
			},
		})
	}))
	defer srv.Close()

	b := NewCodeRabbitBackend("test-api-key", testLogger(), WithAPIURL(srv.URL))
	result, err := b.ReviewDiff(context.Background(), "task-456", "diff content here")
	require.NoError(t, err)

	assert.True(t, result.Passed)
	assert.Equal(t, "All checks passed", result.Summary)

	require.Len(t, result.Comments, 1)
	assert.Equal(t, "main.go", result.Comments[0].FilePath)
	assert.Equal(t, 10, result.Comments[0].Line)
	assert.Equal(t, "Consider adding a comment here", result.Comments[0].Body)
	assert.Equal(t, "info", result.Comments[0].Severity)

	require.Len(t, result.SecurityFindings, 1)
	assert.Equal(t, "SEC-001", result.SecurityFindings[0].RuleID)
	assert.Equal(t, "Potential SQL injection", result.SecurityFindings[0].Description)
	assert.Equal(t, "db.go", result.SecurityFindings[0].FilePath)
	assert.Equal(t, 42, result.SecurityFindings[0].Line)
	assert.Equal(t, "high", result.SecurityFindings[0].Severity)
}

func TestCodeRabbitBackend_ReviewDiff_Failed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crReviewResponse{
			Passed:  false,
			Summary: "Review failed: critical issues found",
			Comments: []crComment{
				{
					FilePath: "auth.go",
					Line:     5,
					Body:     "Hard-coded credentials detected",
					Severity: "critical",
				},
			},
		})
	}))
	defer srv.Close()

	b := NewCodeRabbitBackend("key", testLogger(), WithAPIURL(srv.URL))
	result, err := b.ReviewDiff(context.Background(), "task-789", "bad diff")
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Contains(t, result.Summary, "critical issues")
	require.Len(t, result.Comments, 1)
	assert.Equal(t, "error", result.Comments[0].Severity) // "critical" mapped to "error"
}

func TestCodeRabbitBackend_ReviewDiff_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	b := NewCodeRabbitBackend("key", testLogger(), WithAPIURL(srv.URL))
	_, err := b.ReviewDiff(context.Background(), "task-err", "diff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

func TestMapCommentSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"info", "info"},
		{"warning", "warning"},
		{"error", "error"},
		{"critical", "error"},
		{"INFO", "info"},
		{"unknown", "info"},
		{"", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, mapCommentSeverity(tt.input))
		})
	}
}

func TestMapSecuritySeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"critical", "critical"},
		{"info", "low"},
		{"LOW", "low"},
		{"unknown", "low"},
		{"", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, mapSecuritySeverity(tt.input))
		})
	}
}
