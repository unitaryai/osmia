// Package tournament implements competitive execution with tournament
// selection. Multiple engines execute the same task in parallel, and a
// judge engine evaluates the outputs to select the best result.
package tournament

import "time"

// TournamentState represents the current phase of a tournament or a
// candidate within it.
type TournamentState string

const (
	// StateCompeting indicates candidates are still executing.
	StateCompeting TournamentState = "Competing"
	// StateJudging indicates candidate outputs are being evaluated.
	StateJudging TournamentState = "Judging"
	// StateSelected indicates this candidate was chosen as the winner.
	StateSelected TournamentState = "Selected"
	// StateEliminated indicates this candidate was not selected.
	StateEliminated TournamentState = "Eliminated"
	// StateCancelled indicates the tournament was cancelled before completion.
	StateCancelled TournamentState = "Cancelled"
)

// Tournament tracks the lifecycle of a competitive execution between
// multiple engine candidates for a single task.
type Tournament struct {
	ID               string                      `json:"id"`
	TicketID         string                      `json:"ticket_id"`
	TaskRunIDs       []string                    `json:"task_run_ids"`
	CandidateStates  map[string]TournamentState  `json:"candidate_states"`
	CandidateResults map[string]*CandidateResult `json:"candidate_results,omitempty"`
	JudgeTaskRunID   string                      `json:"judge_task_run_id,omitempty"`
	WinnerTaskRunID  string                      `json:"winner_task_run_id,omitempty"`
	State            TournamentState             `json:"state"`
	Config           TournamentConfig            `json:"config"`
	CreatedAt        time.Time                   `json:"created_at"`
	CompletedAt      *time.Time                  `json:"completed_at,omitempty"`
}

// TournamentConfig holds per-tournament configuration, either from the
// global config or overridden at the ticket level.
type TournamentConfig struct {
	CandidateCount            int      `json:"candidate_count"`
	CandidateEngines          []string `json:"candidate_engines,omitempty"`
	JudgeEngine               string   `json:"judge_engine"`
	EarlyTerminationThreshold float64  `json:"early_termination_threshold"`
	MaxConcurrentTournaments  int      `json:"max_concurrent_tournaments"`
}

// CandidateResult holds the output of a single candidate's execution.
type CandidateResult struct {
	TaskRunID  string        `json:"task_run_id"`
	Engine     string        `json:"engine"`
	Diff       string        `json:"diff"`
	ResultJSON string        `json:"result_json"`
	Summary    string        `json:"summary,omitempty"`
	Success    bool          `json:"success"`
	Cost       float64       `json:"cost"`
	Duration   time.Duration `json:"duration"`
	PRMScores  []int         `json:"prm_scores,omitempty"`
}

// CompletedCount returns the number of candidates that have finished
// execution (have a result recorded).
func (t *Tournament) CompletedCount() int {
	count := 0
	for _, r := range t.CandidateResults {
		if r != nil {
			count++
		}
	}
	return count
}

// IsReadyForJudging returns true when enough candidates have completed to
// meet the early termination threshold.
func (t *Tournament) IsReadyForJudging() bool {
	if t.JudgeTaskRunID != "" {
		return false // already judging
	}
	total := len(t.TaskRunIDs)
	if total == 0 {
		return false
	}
	threshold := t.Config.EarlyTerminationThreshold
	if threshold <= 0 {
		threshold = 0.6
	}
	required := int(float64(total)*threshold + 0.5) // round up
	if required < 1 {
		required = 1
	}
	return t.CompletedCount() >= required
}

// CompletedResults returns the results of all completed candidates in
// task run ID order.
func (t *Tournament) CompletedResults() []*CandidateResult {
	var results []*CandidateResult
	for _, id := range t.TaskRunIDs {
		if r, ok := t.CandidateResults[id]; ok && r != nil {
			results = append(results, r)
		}
	}
	return results
}

// LaggingCandidateIDs returns the task run IDs of candidates that have
// not yet completed. These may be terminated once judging begins.
func (t *Tournament) LaggingCandidateIDs() []string {
	var lagging []string
	for _, id := range t.TaskRunIDs {
		if _, hasResult := t.CandidateResults[id]; !hasResult {
			lagging = append(lagging, id)
		}
	}
	return lagging
}
