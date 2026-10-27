package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_PodAnnotations pins that a malformed agent pod annotation is
// rejected at startup, rather than surfacing as every agent Job failing.
func TestValidate_PodAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantErr     bool
	}{
		{name: "none configured", annotations: nil},
		{name: "valid prefixed key", annotations: map[string]string{"karpenter.sh/do-not-disrupt": "true"}},
		{name: "invalid key", annotations: map[string]string{"not a valid key!": "true"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Execution: ExecutionConfig{PodAnnotations: tt.annotations}}

			err := cfg.Validate()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "execution.pod_annotations")
				return
			}
			assert.NoError(t, err)
		})
	}
}
