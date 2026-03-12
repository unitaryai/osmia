package sessionstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"log/slog"
	"os"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

func TestPerTaskRunPVCStore_Prepare_CreatesPVC(t *testing.T) {
	// Prepare must create a PVC labelled with the TaskRun ID.
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "1Gi", testLogger())

	err := store.Prepare(context.Background(), "tr-create")
	require.NoError(t, err)

	pvcName := pvcNameForTaskRun("tr-create")
	pvc, err := client.CoreV1().PersistentVolumeClaims("default").Get(
		context.Background(), pvcName, metav1.GetOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, "tr-create", pvc.Labels[labelTaskRunID])
	assert.Equal(t, corev1.ReadWriteOnce, pvc.Spec.AccessModes[0])
	assert.Equal(t, resource.MustParse("1Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
}

func TestPerTaskRunPVCStore_Prepare_CustomStorageSize(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "5Gi", testLogger())

	require.NoError(t, store.Prepare(context.Background(), "tr-5gi"))

	pvc, err := client.CoreV1().PersistentVolumeClaims("default").Get(
		context.Background(), pvcNameForTaskRun("tr-5gi"), metav1.GetOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, resource.MustParse("5Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
}

func TestPerTaskRunPVCStore_Prepare_DefaultStorageSize(t *testing.T) {
	// When storageSize is empty the default "1Gi" is used.
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "", testLogger())

	require.NoError(t, store.Prepare(context.Background(), "tr-default"))
	pvc, err := client.CoreV1().PersistentVolumeClaims("default").Get(
		context.Background(), pvcNameForTaskRun("tr-default"), metav1.GetOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, resource.MustParse("1Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
}

func TestPerTaskRunPVCStore_VolumeMounts(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "", testLogger())

	mounts := store.VolumeMounts("tr-vm")
	require.Len(t, mounts, 2)

	pvcName := pvcNameForTaskRun("tr-vm")
	assert.Equal(t, "session-claude", mounts[0].Name)
	assert.Equal(t, pvcName, mounts[0].PVCName)
	assert.Equal(t, sessionDirName, mounts[0].SubPath)

	assert.Equal(t, "session-workspace", mounts[1].Name)
	assert.Equal(t, pvcName, mounts[1].PVCName)
	assert.Equal(t, workspaceDirName, mounts[1].SubPath)
}

func TestPerTaskRunPVCStore_Env(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "", testLogger())

	env := store.Env("tr-env", "sess-id")
	assert.NotEmpty(t, env["CLAUDE_CONFIG_DIR"])
	assert.NotEmpty(t, env["OSMIA_WORKSPACE_DIR"])
	assert.Equal(t, "sess-id", env["OSMIA_SESSION_ID"])
}

func TestPerTaskRunPVCStore_Cleanup_DeletesPVC(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "", testLogger())

	require.NoError(t, store.Prepare(context.Background(), "tr-cleanup"))

	// PVC exists before cleanup.
	pvcName := pvcNameForTaskRun("tr-cleanup")
	_, err := client.CoreV1().PersistentVolumeClaims("default").Get(
		context.Background(), pvcName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	require.NoError(t, store.Cleanup(context.Background(), "tr-cleanup"))

	// PVC must be gone after cleanup.
	_, err = client.CoreV1().PersistentVolumeClaims("default").Get(
		context.Background(), pvcName, metav1.GetOptions{},
	)
	assert.Error(t, err, "PVC should be deleted")
}

func TestPerTaskRunPVCStore_Cleanup_NotFoundIsSuccess(t *testing.T) {
	// Cleanup on a non-existent PVC must succeed (idempotent).
	client := fake.NewSimpleClientset()
	store := NewPerTaskRunPVCStore(client, "default", "", "", testLogger())

	err := store.Cleanup(context.Background(), "tr-does-not-exist")
	assert.NoError(t, err, "not-found should be treated as successful cleanup")
}

func TestPVCNameForTaskRun(t *testing.T) {
	assert.Equal(t, "osmia-session-tr-123", pvcNameForTaskRun("tr-123"))
}

func TestPVCNameForTaskRun_LongID_Truncated(t *testing.T) {
	longID := "this-is-a-very-long-task-run-id-that-is-definitely-longer-than-two-hundred-and-fifty-three-characters-when-the-prefix-is-added-to-it-so-we-need-to-truncate-it-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := pvcNameForTaskRun(longID)
	assert.LessOrEqual(t, len(name), 253)
}
