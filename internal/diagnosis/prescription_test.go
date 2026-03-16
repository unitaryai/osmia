package diagnosis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrescriber_AllFailureModes(t *testing.T) {
	prescriber := NewPrescriber()

	tests := []struct {
		name         string
		mode         FailureMode
		filesChanged int
		wantContains string
	}{
		{
			name:         "wrong approach",
			mode:         WrongApproach,
			wantContains: "Focus specifically on the assigned task",
		},
		{
			name:         "dependency missing",
			mode:         DependencyMissing,
			wantContains: "ensure all required dependencies",
		},
		{
			name:         "test misunderstanding",
			mode:         TestMisunderstanding,
			wantContains: "Do not modify test files",
		},
		{
			name:         "scope creep with file count",
			mode:         ScopeCreep,
			filesChanged: 42,
			wantContains: "42 changed",
		},
		{
			name:         "permission blocked",
			mode:         PermissionBlocked,
			wantContains: "blocked by file or network permissions",
		},
		{
			name:         "model confusion",
			mode:         ModelConfusion,
			wantContains: "step-by-step approach",
		},
		{
			name:         "infra failure",
			mode:         InfraFailure,
			wantContains: "infrastructure issues",
		},
		{
			name:         "unknown",
			mode:         Unknown,
			wantContains: "failed for unclear reasons",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := &Diagnosis{
				Mode:        tt.mode,
				Confidence:  0.80,
				Evidence:    []string{"test evidence"},
				DiagnosedAt: time.Now(),
			}

			result, err := prescriber.Prescribe(diag, tt.filesChanged)
			require.NoError(t, err)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, tt.wantContains)
		})
	}
}

func TestPrescriber_NilDiagnosis(t *testing.T) {
	prescriber := NewPrescriber()
	_, err := prescriber.Prescribe(nil, 0)
	assert.Error(t, err)
}

func TestPrescriber_UnknownModeUsesDefault(t *testing.T) {
	prescriber := NewPrescriber()
	diag := &Diagnosis{
		Mode:        FailureMode("totally_unknown_mode"),
		Confidence:  0.50,
		DiagnosedAt: time.Now(),
	}

	result, err := prescriber.Prescribe(diag, 0)
	require.NoError(t, err)
	assert.Contains(t, result, "failed for unclear reasons")
}

func TestPrescriber_SafeFromInjection(t *testing.T) {
	prescriber := NewPrescriber()
	diag := &Diagnosis{
		Mode:        ScopeCreep,
		Confidence:  0.70,
		Evidence:    []string{"{{.BadTemplate}}", "<script>alert('xss')</script>"},
		DiagnosedAt: time.Now(),
	}

	// The prescription should render safely without executing injected templates.
	result, err := prescriber.Prescribe(diag, 5)
	require.NoError(t, err)
	assert.NotContains(t, result, "<script>")
	assert.NotContains(t, result, "BadTemplate")
	assert.Contains(t, result, "5 changed")
}
