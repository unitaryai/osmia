package secretresolver

import (
	"context"
	"log/slog"
)

// AuditLogger provides structured audit logging for secret resolution
// operations. It logs secret names and metadata but never logs secret values.
type AuditLogger struct {
	logger *slog.Logger
}

// NewAuditLogger creates a new AuditLogger wrapping the given structured logger.
func NewAuditLogger(logger *slog.Logger) *AuditLogger {
	return &AuditLogger{
		logger: logger.With("component", "secret-resolver"),
	}
}

// LogResolutionAttempt logs the start of a secret resolution operation.
// It records the task ID and requested environment variable names only,
// never secret values or URIs.
func (a *AuditLogger) LogResolutionAttempt(ctx context.Context, taskID string, requests []SecretRequest) {
	envNames := make([]string, len(requests))
	for i, req := range requests {
		envNames[i] = req.EnvName
	}
	a.logger.InfoContext(ctx, "secret resolution attempted",
		"task_id", taskID,
		"requested_env_names", envNames,
		"count", len(requests),
	)
}

// LogResolutionSuccess logs a successful secret resolution. It records env
// names and the URI schemes used, but never secret values.
func (a *AuditLogger) LogResolutionSuccess(ctx context.Context, taskID string, resolved []ResolvedSecret) {
	envNames := make([]string, len(resolved))
	for i, rs := range resolved {
		envNames[i] = rs.EnvName
	}
	a.logger.InfoContext(ctx, "secret resolution succeeded",
		"task_id", taskID,
		"resolved_env_names", envNames,
		"count", len(resolved),
	)
}

// LogResolutionFailure logs a failed secret resolution operation.
func (a *AuditLogger) LogResolutionFailure(ctx context.Context, taskID string, err error) {
	a.logger.ErrorContext(ctx, "secret resolution failed",
		"task_id", taskID,
		"error", err.Error(),
	)
}

// LogPolicyViolation logs a policy violation during secret validation.
func (a *AuditLogger) LogPolicyViolation(ctx context.Context, taskID string, req SecretRequest, reason string) {
	a.logger.WarnContext(ctx, "secret policy violation",
		"task_id", taskID,
		"env_name", req.EnvName,
		"reason", reason,
	)
}
