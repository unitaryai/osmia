package diagnosis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/taskrun"
)

func TestRetryBuilder_Build(t *testing.T) {
	ctx := context.Background()
	builder := NewRetryBuilder(diagTestLogger())

	tests := []struct {
		name               string
		originalPrompt     string
		diag               *Diagnosis
		originalEngine     string
		enableEngineSwitch bool
		wantEngine         string
		wantContains       string
	}{
		{
			name:           "basic retry keeps same engine",
			originalPrompt: "fix the login bug",
			diag: &Diagnosis{
				Mode:        WrongApproach,
				Confidence:  0.70,
				Evidence:    []string{"edited wrong files"},
				DiagnosedAt: time.Now(),
			},
			originalEngine: "claude-code",
			wantEngine:     "claude-code",
			wantContains:   "Corrective instructions",
		},
		{
			name:           "engine switch when enabled and suggested",
			originalPrompt: "add the feature",
			diag: &Diagnosis{
				Mode:            ModelConfusion,
				Confidence:      0.80,
				Evidence:        []string{"oscillating tool calls"},
				SuggestedEngine: "codex",
				DiagnosedAt:     time.Now(),
			},
			originalEngine:     "claude-code",
			enableEngineSwitch: true,
			wantEngine:         "codex",
			wantContains:       "step-by-step approach",
		},
		{
			name:           "engine switch ignored when disabled",
			originalPrompt: "add the feature",
			diag: &Diagnosis{
				Mode:            ModelConfusion,
				Confidence:      0.80,
				SuggestedEngine: "codex",
				DiagnosedAt:     time.Now(),
			},
			originalEngine:     "claude-code",
			enableEngineSwitch: false,
			wantEngine:         "claude-code",
		},
		{
			name:           "engine switch ignored when same engine",
			originalPrompt: "task",
			diag: &Diagnosis{
				Mode:            DependencyMissing,
				Confidence:      0.75,
				SuggestedEngine: "claude-code",
				DiagnosedAt:     time.Now(),
			},
			originalEngine:     "claude-code",
			enableEngineSwitch: true,
			wantEngine:         "claude-code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := builder.Build(ctx, tt.originalPrompt, tt.diag, tt.originalEngine, tt.enableEngineSwitch)
			require.NoError(t, err)
			require.NotNil(t, spec)
			assert.Equal(t, tt.wantEngine, spec.Engine)
			assert.Contains(t, spec.Prompt, tt.originalPrompt)
			assert.NotEmpty(t, spec.Reason)
			if tt.wantContains != "" {
				assert.Contains(t, spec.Prompt, tt.wantContains)
			}
		})
	}
}

func TestRetryBuilder_NilDiagnosis(t *testing.T) {
	ctx := context.Background()
	builder := NewRetryBuilder(diagTestLogger())
	_, err := builder.Build(ctx, "prompt", nil, "claude-code", false)
	assert.Error(t, err)
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name         string
		history      []taskrun.DiagnosisRecord
		current      *Diagnosis
		maxDiagnoses int
		want         bool
	}{
		{
			name:    "nil current returns false",
			current: nil,
			want:    false,
		},
		{
			name: "first diagnosis allows retry",
			current: &Diagnosis{
				Mode: DependencyMissing,
			},
			maxDiagnoses: 3,
			want:         true,
		},
		{
			name: "same failure mode prevents retry",
			history: []taskrun.DiagnosisRecord{
				{Mode: string(DependencyMissing)},
			},
			current: &Diagnosis{
				Mode: DependencyMissing,
			},
			maxDiagnoses: 3,
			want:         false,
		},
		{
			name: "different failure mode allows retry",
			history: []taskrun.DiagnosisRecord{
				{Mode: string(WrongApproach)},
			},
			current: &Diagnosis{
				Mode: DependencyMissing,
			},
			maxDiagnoses: 3,
			want:         true,
		},
		{
			name: "max diagnoses exceeded",
			history: []taskrun.DiagnosisRecord{
				{Mode: string(WrongApproach)},
				{Mode: string(DependencyMissing)},
				{Mode: string(ScopeCreep)},
			},
			current: &Diagnosis{
				Mode: TestMisunderstanding,
			},
			maxDiagnoses: 3,
			want:         false,
		},
		{
			name: "infra failure prevents retry",
			current: &Diagnosis{
				Mode: InfraFailure,
			},
			maxDiagnoses: 3,
			want:         false,
		},
		{
			name: "unknown mode allows retry",
			current: &Diagnosis{
				Mode: Unknown,
			},
			maxDiagnoses: 3,
			want:         true,
		},
		{
			name: "zero max diagnoses means no limit",
			history: []taskrun.DiagnosisRecord{
				{Mode: string(WrongApproach)},
				{Mode: string(DependencyMissing)},
			},
			current: &Diagnosis{
				Mode: ScopeCreep,
			},
			maxDiagnoses: 0,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRetry(tt.history, tt.current, tt.maxDiagnoses)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToDiagnosisRecord(t *testing.T) {
	t.Run("converts diagnosis to record", func(t *testing.T) {
		now := time.Now()
		d := &Diagnosis{
			Mode:            WrongApproach,
			Confidence:      0.75,
			Evidence:        []string{"evidence1", "evidence2"},
			Prescription:    "fix it",
			SuggestedEngine: "codex",
			DiagnosedAt:     now,
		}

		record := ToDiagnosisRecord(d)
		assert.Equal(t, string(WrongApproach), record.Mode)
		assert.Equal(t, 0.75, record.Confidence)
		assert.Equal(t, []string{"evidence1", "evidence2"}, record.Evidence)
		assert.Equal(t, "fix it", record.Prescription)
		assert.Equal(t, "codex", record.SuggestedEngine)
		assert.Equal(t, now, record.DiagnosedAt)
	})

	t.Run("nil returns empty record", func(t *testing.T) {
		record := ToDiagnosisRecord(nil)
		assert.Empty(t, record.Mode)
	})
}
