package jobbuilder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_ServiceAccountName(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{name: "configured account names the agent pod", opts: []Option{WithServiceAccountName("osmia-agent")}, want: "osmia-agent"},
		{name: "no account named leaves the namespace default account", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := NewJobBuilder("default", tt.opts...).Build("tr-sa", "claude-code", validSpec())

			require.NoError(t, err)
			assert.Equal(t, tt.want, job.Spec.Template.Spec.ServiceAccountName)
			assert.Equal(t, ptrBool(false), job.Spec.Template.Spec.AutomountServiceAccountToken)
		})
	}
}
