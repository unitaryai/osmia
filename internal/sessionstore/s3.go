package sessionstore

import (
	"context"
	"fmt"

	"github.com/unitaryai/osmia/pkg/engine"
)

// S3Store will implement SessionStore using Amazon S3 (or any S3-compatible
// store) for session data persistence. The implementation uses an init
// container to download session data before the agent starts, and a lifecycle
// PostStop hook to upload updated session data on exit.
//
// TODO: Full implementation requires init container support in ExecutionSpec.
// Until then, Prepare returns an error directing operators to use the
// shared-pvc or per-taskrun-pvc backends instead.
type S3Store struct {
	bucket string
	prefix string
}

// NewS3Store returns an S3Store for the given bucket and prefix.
// prefix defaults to "osmia-sessions/" when empty.
func NewS3Store(bucket, prefix string) *S3Store {
	if prefix == "" {
		prefix = "osmia-sessions/"
	}
	return &S3Store{bucket: bucket, prefix: prefix}
}

// Prepare returns an error because the S3 backend requires init container
// support which is not yet implemented in ExecutionSpec.
func (s *S3Store) Prepare(_ context.Context, taskRunID string) error {
	return fmt.Errorf(
		"s3 session persistence backend is not yet supported (task_run_id=%s): "+
			"use shared-pvc or per-taskrun-pvc instead",
		taskRunID,
	)
}

// VolumeMounts returns no additional volumes for the S3 backend.
func (s *S3Store) VolumeMounts(_ string) []engine.VolumeMount {
	return nil
}

// Env returns no additional environment variables for the S3 backend.
func (s *S3Store) Env(_, _ string) map[string]string {
	return nil
}

// Cleanup is a no-op for the S3 backend (TTL-based deletion handled externally).
func (s *S3Store) Cleanup(_ context.Context, _ string) error {
	return nil
}
