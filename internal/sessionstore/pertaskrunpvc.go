package sessionstore

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// perTaskRunSessionPath is the path inside the per-TaskRun PVC where the
	// Claude config directory is mounted.
	perTaskRunSessionPath = "/session"

	// perTaskRunWorkspacePath is the path inside the per-TaskRun PVC where
	// the workspace is mounted.
	perTaskRunWorkspacePath = "/workspace-persist"

	// defaultPVCStorageSize is the default PVC size for per-TaskRun PVCs.
	defaultPVCStorageSize = "1Gi"

	// labelTaskRunID is the label used to associate PVCs with a TaskRun.
	labelTaskRunID = "osmia.io/task-run-id"
)

// PerTaskRunPVCStore implements SessionStore by dynamically creating and
// deleting a dedicated PVC for each TaskRun. The PVC is created on Prepare
// and deleted on Cleanup, so storage is never shared between TaskRuns.
type PerTaskRunPVCStore struct {
	client       kubernetes.Interface
	namespace    string
	storageClass string
	storageSize  string
	logger       *slog.Logger
}

// NewPerTaskRunPVCStore returns a PerTaskRunPVCStore that creates PVCs in the
// given namespace. storageClass may be empty to use the cluster default.
// storageSize defaults to "1Gi" when empty.
func NewPerTaskRunPVCStore(
	client kubernetes.Interface,
	namespace string,
	storageClass string,
	storageSize string,
	logger *slog.Logger,
) *PerTaskRunPVCStore {
	if storageSize == "" {
		storageSize = defaultPVCStorageSize
	}
	return &PerTaskRunPVCStore{
		client:       client,
		namespace:    namespace,
		storageClass: storageClass,
		storageSize:  storageSize,
		logger:       logger,
	}
}

// Prepare creates a dedicated PVC for the TaskRun. The PVC is labelled with
// the TaskRun ID so the cleaner can locate and remove it later.
func (s *PerTaskRunPVCStore) Prepare(ctx context.Context, taskRunID string) error {
	pvcName := pvcNameForTaskRun(taskRunID)
	qty, err := resource.ParseQuantity(s.storageSize)
	if err != nil {
		return fmt.Errorf("invalid storage size %q: %w", s.storageSize, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: s.namespace,
			Labels: map[string]string{
				labelTaskRunID: taskRunID,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}

	if s.storageClass != "" {
		pvc.Spec.StorageClassName = &s.storageClass
	}

	_, err = s.client.CoreV1().PersistentVolumeClaims(s.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating session PVC for task run %q: %w", taskRunID, err)
	}

	s.logger.InfoContext(ctx, "created session PVC",
		"task_run_id", taskRunID,
		"pvc_name", pvcName,
	)
	return nil
}

// VolumeMounts returns two mounts from the per-TaskRun PVC: one for the Claude
// config directory and one for the workspace.
func (s *PerTaskRunPVCStore) VolumeMounts(taskRunID string) []engine.VolumeMount {
	pvcName := pvcNameForTaskRun(taskRunID)
	return []engine.VolumeMount{
		{
			Name:      "session-claude",
			MountPath: perTaskRunSessionPath,
			SubPath:   sessionDirName,
			PVCName:   pvcName,
		},
		{
			Name:      "session-workspace",
			MountPath: perTaskRunWorkspacePath,
			SubPath:   workspaceDirName,
			PVCName:   pvcName,
		},
	}
}

// Env returns environment variables directing the agent to the persisted
// directories on the per-TaskRun PVC.
func (s *PerTaskRunPVCStore) Env(taskRunID, sessionID string) map[string]string {
	env := map[string]string{
		"CLAUDE_CONFIG_DIR":    filepath.Join(perTaskRunSessionPath),
		"OSMIA_WORKSPACE_DIR":  filepath.Join(perTaskRunWorkspacePath),
	}
	if sessionID != "" {
		env["OSMIA_SESSION_ID"] = sessionID
	}
	return env
}

// Cleanup deletes the per-TaskRun PVC. Safe to call even if the PVC no longer
// exists (not-found errors are silently ignored).
func (s *PerTaskRunPVCStore) Cleanup(ctx context.Context, taskRunID string) error {
	pvcName := pvcNameForTaskRun(taskRunID)
	err := s.client.CoreV1().PersistentVolumeClaims(s.namespace).Delete(ctx, pvcName, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		s.logger.InfoContext(ctx, "session PVC already deleted",
			"task_run_id", taskRunID,
			"pvc_name", pvcName,
		)
		return nil
	}
	if err != nil {
		s.logger.WarnContext(ctx, "failed to delete session PVC",
			"task_run_id", taskRunID,
			"pvc_name", pvcName,
			"error", err,
		)
		return fmt.Errorf("deleting session PVC for task run %q: %w", taskRunID, err)
	}
	s.logger.InfoContext(ctx, "deleted session PVC",
		"task_run_id", taskRunID,
		"pvc_name", pvcName,
	)
	return nil
}

// pvcNameForTaskRun returns the deterministic PVC name for a TaskRun.
func pvcNameForTaskRun(taskRunID string) string {
	name := fmt.Sprintf("osmia-session-%s", taskRunID)
	// Kubernetes names must not exceed 253 characters.
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}
