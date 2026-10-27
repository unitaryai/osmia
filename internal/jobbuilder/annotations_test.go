package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const doNotDisrupt = "karpenter.sh/do-not-disrupt"

func TestMergePodAnnotations(t *testing.T) {
	t.Run("nothing to set gives nil, keeping Job specs unchanged", func(t *testing.T) {
		assert.Nil(t, MergePodAnnotations(nil, nil))
		assert.Nil(t, MergePodAnnotations(map[string]string{}, map[string]string{}))
	})

	t.Run("Osmia's own annotations win on a clash", func(t *testing.T) {
		got := MergePodAnnotations(
			map[string]string{"osmia.io/sandbox-claim": "false", doNotDisrupt: "true"},
			map[string]string{"osmia.io/sandbox-claim": "true"},
		)
		assert.Equal(t, map[string]string{"osmia.io/sandbox-claim": "true", doNotDisrupt: "true"}, got)
	})

	t.Run("returns a fresh map, so one Job cannot alter the next", func(t *testing.T) {
		extra := map[string]string{doNotDisrupt: "true"}
		got := MergePodAnnotations(extra, nil)
		got["mutated"] = "yes"
		assert.NotContains(t, extra, "mutated")
	})
}

func TestBuild_PodAnnotations(t *testing.T) {
	t.Run("configured annotations reach the agent pod", func(t *testing.T) {
		job, err := NewJobBuilder("default", WithPodAnnotations(map[string]string{doNotDisrupt: "true"})).
			Build("tr-ann", "claude-code", validSpec())
		require.NoError(t, err)
		assert.Equal(t, "true", job.Spec.Template.Annotations[doNotDisrupt])
	})

	t.Run("no configuration leaves the pod without annotations, as before", func(t *testing.T) {
		job, err := NewJobBuilder("default").Build("tr-ann", "claude-code", validSpec())
		require.NoError(t, err)
		assert.Nil(t, job.Spec.Template.Annotations)
	})
}
