package prm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterventionDecider_Decide(t *testing.T) {
	tests := []struct {
		name       string
		scores     []int
		wantAction InterventionAction
	}{
		{
			name:       "high score continues",
			scores:     []int{8, 9, 8},
			wantAction: ActionContinue,
		},
		{
			name:       "score at nudge threshold continues",
			scores:     []int{7, 7, 7},
			wantAction: ActionContinue,
		},
		{
			name:       "declining below threshold nudges",
			scores:     []int{8, 6, 5, 4},
			wantAction: ActionNudge,
		},
		{
			name:       "very low score with sustained decline escalates",
			scores:     []int{6, 5, 3, 2},
			wantAction: ActionEscalate,
		},
		{
			name:       "low score without decline continues",
			scores:     []int{5, 5, 5},
			wantAction: ActionContinue,
		},
		{
			name:       "oscillation below threshold nudges",
			scores:     []int{3, 6, 3, 6, 3},
			wantAction: ActionNudge,
		},
		{
			name:       "recovery above threshold continues",
			scores:     []int{3, 5, 7, 9},
			wantAction: ActionContinue,
		},
		{
			name:       "empty trajectory continues",
			scores:     nil,
			wantAction: ActionContinue,
		},
		{
			name:       "single low score continues",
			scores:     []int{2},
			wantAction: ActionContinue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider := NewInterventionDecider(7, 3, "/workspace/.osmia-hint.md")
			traj := NewTrajectory(50)
			for _, s := range tt.scores {
				traj.AddScore(makeScore(s))
			}

			intervention := decider.Decide(traj)
			require.NotNil(t, intervention)
			assert.Equal(t, tt.wantAction, intervention.Action)
			assert.NotEmpty(t, intervention.Reason)

			if intervention.Action == ActionNudge {
				assert.NotEmpty(t, intervention.HintContent, "nudge should include hint content")
			}
		})
	}
}

func TestInterventionDecider_HintFilePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantPath string
	}{
		{
			name:     "custom path",
			path:     "/tmp/hint.md",
			wantPath: "/tmp/hint.md",
		},
		{
			name:     "empty path uses default",
			path:     "",
			wantPath: "/workspace/.osmia-hint.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider := NewInterventionDecider(7, 3, tt.path)
			assert.Equal(t, tt.wantPath, decider.HintFilePath())
		})
	}
}

func TestBuildHintContent(t *testing.T) {
	tests := []struct {
		name    string
		pattern TrajectoryPattern
		wantIn  string
	}{
		{
			name:    "sustained decline hint",
			pattern: PatternSustainedDecline,
			wantIn:  "declining steadily",
		},
		{
			name:    "oscillation hint",
			pattern: PatternOscillation,
			wantIn:  "oscillating",
		},
		{
			name:    "plateau hint",
			pattern: PatternPlateau,
			wantIn:  "stalled",
		},
		{
			name:    "default hint",
			pattern: PatternNone,
			wantIn:  "reviewing your recent approach",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider := NewInterventionDecider(7, 3, "")
			score := &StepScore{Score: 4, Reasoning: "test"}
			hint := decider.buildHintContent(score, tt.pattern)
			assert.Contains(t, hint, tt.wantIn)
			assert.Contains(t, hint, "4/10")
		})
	}
}
