package secretresolver

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditLoggerNeverLogsValues(t *testing.T) {
	secretValue := "sk_test_super_secret_value_12345"
	secretURI := "vault://secret/data/stripe#key"

	tests := []struct {
		name   string
		logFn  func(audit *AuditLogger, buf *bytes.Buffer)
		mustIn []string // strings that must appear in the log
	}{
		{
			name: "resolution attempt logs env names not URIs",
			logFn: func(audit *AuditLogger, _ *bytes.Buffer) {
				audit.LogResolutionAttempt(context.Background(), "task-1", []SecretRequest{
					{EnvName: "STRIPE_API_KEY", URI: secretURI},
				})
			},
			mustIn: []string{"STRIPE_API_KEY", "task-1"},
		},
		{
			name: "resolution success logs env names not values",
			logFn: func(audit *AuditLogger, _ *bytes.Buffer) {
				audit.LogResolutionSuccess(context.Background(), "task-2", []ResolvedSecret{
					{EnvName: "STRIPE_API_KEY", Value: secretValue},
				})
			},
			mustIn: []string{"STRIPE_API_KEY", "task-2"},
		},
		{
			name: "resolution failure logs error",
			logFn: func(audit *AuditLogger, _ *bytes.Buffer) {
				audit.LogResolutionFailure(context.Background(), "task-3", fmt.Errorf("backend unavailable"))
			},
			mustIn: []string{"task-3", "backend unavailable"},
		},
		{
			name: "policy violation logs env name and reason",
			logFn: func(audit *AuditLogger, _ *bytes.Buffer) {
				audit.LogPolicyViolation(context.Background(), "task-4", SecretRequest{
					EnvName: "PATH",
					URI:     secretURI,
				}, "blocked env name")
			},
			mustIn: []string{"task-4", "PATH", "blocked env name"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			audit := NewAuditLogger(logger)

			tt.logFn(audit, &buf)

			output := buf.String()

			// Verify expected fields are present.
			for _, expected := range tt.mustIn {
				assert.Contains(t, output, expected)
			}

			// Critically, verify secret values are never logged.
			require.NotContains(t, output, secretValue,
				"secret value must never appear in audit logs")
		})
	}
}
