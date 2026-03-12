package sessionstore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Cleaner periodically removes stale session data whose TTL has elapsed.
// It runs as a background goroutine started by the controller when session
// persistence is enabled.
type Cleaner struct {
	backend    string
	pvcRootDir string // for shared-pvc: local path to the mounted PVC
	k8sClient  kubernetes.Interface
	namespace  string
	ttl        time.Duration
	interval   time.Duration
	logger     *slog.Logger
}

// CleanerConfig holds configuration for the session cleaner.
type CleanerConfig struct {
	// Backend is the session persistence backend ("shared-pvc", "per-taskrun-pvc").
	Backend string
	// PVCRootDir is the local path where the shared PVC is mounted inside the
	// controller pod. Only used when Backend is "shared-pvc". This path must
	// correspond to a volume mount in the Helm deployment template.
	PVCRootDir string
	// K8sClient is used to list and delete PVCs for the per-taskrun-pvc backend.
	K8sClient kubernetes.Interface
	// Namespace is the Kubernetes namespace where session PVCs are created.
	Namespace string
	// TTL is how long to retain session data after a TaskRun becomes terminal.
	// Defaults to 24 hours.
	TTL time.Duration
	// Interval is how often to run the cleanup sweep. Defaults to 1 hour.
	Interval time.Duration
}

// NewCleaner creates a Cleaner with the given configuration.
func NewCleaner(cfg CleanerConfig, logger *slog.Logger) *Cleaner {
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	return &Cleaner{
		backend:    cfg.Backend,
		pvcRootDir: cfg.PVCRootDir,
		k8sClient:  cfg.K8sClient,
		namespace:  cfg.Namespace,
		ttl:        cfg.TTL,
		interval:   cfg.Interval,
		logger:     logger,
	}
}

// Run starts the periodic cleanup loop. It blocks until ctx is cancelled.
func (c *Cleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sweep(ctx)
		}
	}
}

// sweep performs a single cleanup pass over stale session data.
func (c *Cleaner) sweep(ctx context.Context) {
	switch c.backend {
	case "shared-pvc":
		c.sweepSharedPVC(ctx)
	case "per-taskrun-pvc":
		c.sweepPerTaskRunPVCs(ctx)
	}
}

// sweepSharedPVC removes subdirectories on the shared PVC that are older
// than the TTL. Each subdirectory corresponds to one TaskRun.
//
// IMPORTANT: the controller pod must mount the shared PVC at PVCRootDir
// for this to work. The Helm chart conditionally adds the volume and mount
// when sessionPersistence.backend is "shared-pvc".
func (c *Cleaner) sweepSharedPVC(ctx context.Context) {
	if c.pvcRootDir == "" {
		c.logger.WarnContext(ctx, "shared-pvc cleaner: pvc_root_dir not configured, skipping sweep")
		return
	}
	entries, err := os.ReadDir(c.pvcRootDir)
	if err != nil {
		c.logger.WarnContext(ctx, "shared-pvc cleaner: failed to read PVC root dir",
			"dir", c.pvcRootDir,
			"error", err,
		)
		return
	}
	cutoff := time.Now().Add(-c.ttl)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(c.pvcRootDir, entry.Name())
		lastActivity := newestModTime(dirPath)
		if lastActivity.IsZero() {
			// Could not determine activity time; skip to avoid deleting active sessions.
			continue
		}
		if lastActivity.Before(cutoff) {
			if err := os.RemoveAll(dirPath); err != nil {
				c.logger.WarnContext(ctx, "shared-pvc cleaner: failed to remove stale session dir",
					"dir", dirPath,
					"error", err,
				)
			} else {
				c.logger.InfoContext(ctx, "shared-pvc cleaner: removed stale session dir",
					"dir", dirPath,
					"last_activity", lastActivity,
				)
			}
		}
	}
}

// newestModTime walks dir recursively and returns the most recent modification
// time among all files and directories. A recursive walk is necessary because
// directory mtime only updates when entries are added or removed — not when
// existing files within them are modified (e.g. appending to JSONL session
// files). By checking actual file mtimes we get an accurate activity signal.
func newestModTime(dir string) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip entries we cannot stat
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}

// sweepPerTaskRunPVCs lists PVCs labelled with osmia.io/task-run-id and
// deletes those whose creation timestamp is older than the TTL.
//
// We intentionally do NOT filter by PVC phase. When an agent pod finishes
// and is deleted, the PVC remains in Bound phase — there is no automatic
// transition to Released. Filtering by phase would cause PVCs to accumulate
// indefinitely. Age-based deletion is safe: by the time the TTL fires,
// the TaskRun is guaranteed to be terminal and no pod is referencing the PVC.
func (c *Cleaner) sweepPerTaskRunPVCs(ctx context.Context) {
	if c.k8sClient == nil {
		return
	}
	pvcs, err := c.k8sClient.CoreV1().PersistentVolumeClaims(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelTaskRunID,
	})
	if err != nil {
		c.logger.WarnContext(ctx, "per-taskrun-pvc cleaner: failed to list session PVCs",
			"error", err,
		)
		return
	}
	cutoff := time.Now().Add(-c.ttl)
	for _, pvc := range pvcs.Items {
		if pvc.CreationTimestamp.Time.Before(cutoff) {
			if err := c.k8sClient.CoreV1().PersistentVolumeClaims(c.namespace).Delete(
				ctx, pvc.Name, metav1.DeleteOptions{},
			); err != nil {
				c.logger.WarnContext(ctx, "per-taskrun-pvc cleaner: failed to delete stale PVC",
					"pvc", pvc.Name,
					"error", err,
				)
			} else {
				c.logger.InfoContext(ctx, "per-taskrun-pvc cleaner: deleted stale session PVC",
					"pvc", pvc.Name,
					"created_at", pvc.CreationTimestamp.Time,
				)
			}
		}
	}
}
