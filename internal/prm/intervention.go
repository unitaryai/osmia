package prm

import "fmt"

// InterventionAction describes what the controller should do in response
// to PRM evaluation.
type InterventionAction string

const (
	// ActionContinue means no intervention is needed.
	ActionContinue InterventionAction = "continue"
	// ActionNudge means write a hint file to guide the agent.
	ActionNudge InterventionAction = "nudge"
	// ActionEscalate means the agent should be terminated via the watchdog.
	ActionEscalate InterventionAction = "escalate"
)

// Intervention describes a recommended action based on trajectory analysis.
type Intervention struct {
	// Action is the recommended intervention type.
	Action InterventionAction
	// HintContent is the content to write to the hint file (for nudges).
	HintContent string
	// Reason explains why this intervention was chosen.
	Reason string
}

// InterventionDecider determines what intervention, if any, to take based
// on the current trajectory and score thresholds.
type InterventionDecider struct {
	nudgeThreshold    int
	escalateThreshold int
	hintFilePath      string
}

// NewInterventionDecider creates a decider with the given score thresholds.
// Scores at or above nudgeThreshold need no intervention. Scores below
// escalateThreshold with a sustained decline trigger escalation.
func NewInterventionDecider(nudgeThreshold, escalateThreshold int, hintFilePath string) *InterventionDecider {
	if hintFilePath == "" {
		hintFilePath = "/workspace/.osmia-hint.md"
	}
	return &InterventionDecider{
		nudgeThreshold:    nudgeThreshold,
		escalateThreshold: escalateThreshold,
		hintFilePath:      hintFilePath,
	}
}

// Decide evaluates the trajectory and returns an intervention recommendation.
// The decision logic is:
//   - Score >= nudgeThreshold: continue (no action)
//   - Score < escalateThreshold + sustained decline: escalate
//   - Score < nudgeThreshold + declining/oscillating: nudge
//   - Otherwise: continue
func (d *InterventionDecider) Decide(traj *Trajectory) *Intervention {
	latest := traj.Latest()
	if latest == nil {
		return &Intervention{
			Action: ActionContinue,
			Reason: "no scores available",
		}
	}

	score := latest.Score
	pattern := traj.Pattern()
	trend := traj.CurrentTrend()

	// High score: no intervention needed.
	if score >= d.nudgeThreshold {
		return &Intervention{
			Action: ActionContinue,
			Reason: fmt.Sprintf("score %d is above nudge threshold %d", score, d.nudgeThreshold),
		}
	}

	// Very low score with sustained decline: escalate to watchdog.
	if score <= d.escalateThreshold && pattern == PatternSustainedDecline {
		return &Intervention{
			Action: ActionEscalate,
			Reason: fmt.Sprintf("score %d with sustained decline warrants escalation", score),
		}
	}

	// Moderate score with negative signals: nudge.
	if trend == TrendDeclining || pattern == PatternOscillation || pattern == PatternSustainedDecline || pattern == PatternPlateau {
		return &Intervention{
			Action:      ActionNudge,
			HintContent: d.buildHintContent(latest, pattern),
			Reason:      fmt.Sprintf("score %d with %s trend and %s pattern", score, trend, pattern),
		}
	}

	// Low score but no strong negative pattern yet: continue and observe.
	return &Intervention{
		Action: ActionContinue,
		Reason: fmt.Sprintf("score %d is below threshold but no negative pattern detected", score),
	}
}

// HintFilePath returns the configured path for the hint file.
func (d *InterventionDecider) HintFilePath() string {
	return d.hintFilePath
}

// buildHintContent generates guidance for the agent based on the current
// score and trajectory pattern.
func (d *InterventionDecider) buildHintContent(latest *StepScore, pattern TrajectoryPattern) string {
	hint := "# Osmia Agent Guidance\n\n"
	hint += fmt.Sprintf("Your recent approach scored %d/10. ", latest.Score)

	switch pattern {
	case PatternSustainedDecline:
		hint += "Your effectiveness has been declining steadily. "
		hint += "Consider stepping back and re-reading the requirements before continuing.\n\n"
		hint += "Suggestions:\n"
		hint += "- Re-read the original task description\n"
		hint += "- Review what you have changed so far\n"
		hint += "- Consider a different approach\n"
	case PatternOscillation:
		hint += "Your approach appears to be oscillating without making steady progress. "
		hint += "Try committing to a single strategy rather than switching back and forth.\n\n"
		hint += "Suggestions:\n"
		hint += "- Pick one approach and follow through\n"
		hint += "- Write a brief plan before making changes\n"
		hint += "- Run tests to verify each change\n"
	case PatternPlateau:
		hint += "Your progress appears to have stalled. "
		hint += "Consider trying a different approach.\n\n"
		hint += "Suggestions:\n"
		hint += "- Look at the problem from a different angle\n"
		hint += "- Search for similar patterns in the codebase\n"
		hint += "- Break the task into smaller steps\n"
	default:
		hint += "Consider reviewing your recent approach.\n\n"
		hint += "Suggestions:\n"
		hint += "- Read before editing\n"
		hint += "- Test after making changes\n"
		hint += "- Avoid repeating the same operation\n"
	}

	return hint
}
