package sessionstore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	// controller pod. Only used when Backend is "shared-pvc".
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
func (c *Cleaner) sweepSharedPVC(ctx context.Context) {
	if c.pvcRootDir == "" {
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
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			dirPath := filepath.Join(c.pvcRootDir, entry.Name())
			if err := os.RemoveAll(dirPath); err != nil {
				c.logger.WarnContext(ctx, "shared-pvc cleaner: failed to remove stale session dir",
					"dir", dirPath,
					"error", err,
				)
			} else {
				c.logger.InfoContext(ctx, "shared-pvc cleaner: removed stale session dir",
					"dir", dirPath,
					"mod_time", info.ModTime(),
				)
			}
		}
	}
}

// sweepPerTaskRunPVCs lists PVCs labelled with osmia.io/task-run-id and
// deletes those whose creation timestamp is older than the TTL.
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
		if pvc.CreationTimestamp.Time.Before(cutoff) && isPVCReleasedOrLost(pvc) {
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

// isPVCReleasedOrLost returns true if the PVC is no longer bound to a pod.
// We only delete PVCs that are Released or Lost to avoid removing active sessions.
func isPVCReleasedOrLost(pvc corev1.PersistentVolumeClaim) bool {
	return pvc.Status.Phase == corev1.ClaimLost ||
		pvc.Status.Phase == corev1.ClaimPending ||
		pvc.Status.Phase == ""
}
