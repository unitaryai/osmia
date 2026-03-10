package tournament

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/unitaryai/osmia/internal/metrics"
)

// Coordinator manages the lifecycle of tournaments, from creation through
// candidate completion, judging, and winner selection.
type Coordinator struct {
	mu          sync.Mutex
	logger      *slog.Logger
	tournaments map[string]*Tournament
}

// NewCoordinator creates a tournament Coordinator.
func NewCoordinator(logger *slog.Logger) *Coordinator {
	return &Coordinator{
		logger:      logger,
		tournaments: make(map[string]*Tournament),
	}
}

// StartTournament creates a new tournament for the given ticket with the
// specified candidate task run IDs. It returns the tournament ready for
// candidate jobs to be launched.
func (c *Coordinator) StartTournament(
	_ context.Context,
	tournamentID string,
	ticketID string,
	candidateTaskRunIDs []string,
	cfg TournamentConfig,
) (*Tournament, error) {
	if len(candidateTaskRunIDs) < 2 {
		return nil, fmt.Errorf("tournament requires at least 2 candidates, got %d", len(candidateTaskRunIDs))
	}
	if tournamentID == "" {
		return nil, fmt.Errorf("tournament ID must not be empty")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.tournaments[tournamentID]; exists {
		return nil, fmt.Errorf("tournament %q already exists", tournamentID)
	}

	candidateStates := make(map[string]TournamentState, len(candidateTaskRunIDs))
	candidateResults := make(map[string]*CandidateResult, len(candidateTaskRunIDs))
	for _, id := range candidateTaskRunIDs {
		candidateStates[id] = StateCompeting
	}

	t := &Tournament{
		ID:               tournamentID,
		TicketID:         ticketID,
		TaskRunIDs:       candidateTaskRunIDs,
		CandidateStates:  candidateStates,
		CandidateResults: candidateResults,
		State:            StateCompeting,
		Config:           cfg,
		CreatedAt:        time.Now(),
	}

	c.tournaments[tournamentID] = t

	metrics.TournamentTotal.Inc()

	c.logger.Info("tournament started",
		"tournament_id", tournamentID,
		"ticket_id", ticketID,
		"candidates", len(candidateTaskRunIDs),
	)

	return t, nil
}

// OnCandidateComplete records a completed candidate result and checks whether
// enough candidates have finished to trigger the judging phase. Returns true
// if judging should begin.
func (c *Coordinator) OnCandidateComplete(
	_ context.Context,
	tournamentID string,
	result *CandidateResult,
) (readyForJudging bool, err error) {
	if result == nil {
		return false, fmt.Errorf("candidate result must not be nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.tournaments[tournamentID]
	if !ok {
		return false, fmt.Errorf("tournament %q not found", tournamentID)
	}

	if t.State != StateCompeting {
		return false, fmt.Errorf("tournament %q is not in competing state (current: %s)", tournamentID, t.State)
	}

	if _, exists := t.CandidateStates[result.TaskRunID]; !exists {
		return false, fmt.Errorf("task run %q is not a candidate in tournament %q", result.TaskRunID, tournamentID)
	}

	t.CandidateResults[result.TaskRunID] = result
	metrics.TournamentCandidatesTotal.WithLabelValues(result.Engine).Inc()

	c.logger.Info("candidate completed",
		"tournament_id", tournamentID,
		"task_run_id", result.TaskRunID,
		"engine", result.Engine,
		"success", result.Success,
		"cost_usd", result.Cost,
		"completed", t.CompletedCount(),
		"total", len(t.TaskRunIDs),
	)

	return t.IsReadyForJudging(), nil
}

// BeginJudging transitions the tournament to the judging state. Returns
// the completed candidate results for the judge to evaluate.
func (c *Coordinator) BeginJudging(
	_ context.Context,
	tournamentID string,
	judgeTaskRunID string,
) ([]*CandidateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.tournaments[tournamentID]
	if !ok {
		return nil, fmt.Errorf("tournament %q not found", tournamentID)
	}

	if t.State != StateCompeting {
		return nil, fmt.Errorf("tournament %q is not in competing state", tournamentID)
	}

	t.State = StateJudging
	t.JudgeTaskRunID = judgeTaskRunID

	// Mark uncompleted candidates as eliminated.
	for id := range t.CandidateStates {
		if _, hasResult := t.CandidateResults[id]; !hasResult {
			t.CandidateStates[id] = StateEliminated
		}
	}

	c.logger.Info("tournament entered judging phase",
		"tournament_id", tournamentID,
		"judge_task_run_id", judgeTaskRunID,
		"candidates_evaluated", t.CompletedCount(),
	)

	return t.CompletedResults(), nil
}

// SelectWinner marks the winning candidate and completes the tournament.
// All non-winning candidates are marked as eliminated.
func (c *Coordinator) SelectWinner(
	_ context.Context,
	tournamentID string,
	winnerTaskRunID string,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.tournaments[tournamentID]
	if !ok {
		return fmt.Errorf("tournament %q not found", tournamentID)
	}

	if t.State != StateJudging {
		return fmt.Errorf("tournament %q is not in judging state (current: %s)", tournamentID, t.State)
	}

	if _, exists := t.CandidateResults[winnerTaskRunID]; !exists {
		return fmt.Errorf("winner %q has no result in tournament %q", winnerTaskRunID, tournamentID)
	}

	// Mark all candidates as eliminated, then override the winner.
	for id := range t.CandidateStates {
		t.CandidateStates[id] = StateEliminated
	}
	t.CandidateStates[winnerTaskRunID] = StateSelected
	t.WinnerTaskRunID = winnerTaskRunID
	t.State = StateSelected
	now := time.Now()
	t.CompletedAt = &now

	// Record metrics.
	winner := t.CandidateResults[winnerTaskRunID]
	metrics.TournamentWinnerEngine.WithLabelValues(winner.Engine).Inc()

	var totalCost float64
	for _, r := range t.CandidateResults {
		if r != nil {
			totalCost += r.Cost
		}
	}
	metrics.TournamentCostTotal.Observe(totalCost)
	metrics.TournamentDurationSeconds.Observe(now.Sub(t.CreatedAt).Seconds())

	c.logger.Info("tournament winner selected",
		"tournament_id", tournamentID,
		"winner_task_run_id", winnerTaskRunID,
		"winner_engine", winner.Engine,
		"total_cost_usd", totalCost,
		"duration_seconds", now.Sub(t.CreatedAt).Seconds(),
	)

	return nil
}

// CancelTournament cancels a tournament, marking all candidates as eliminated.
func (c *Coordinator) CancelTournament(_ context.Context, tournamentID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.tournaments[tournamentID]
	if !ok {
		return fmt.Errorf("tournament %q not found", tournamentID)
	}

	for id := range t.CandidateStates {
		t.CandidateStates[id] = StateEliminated
	}
	t.State = StateCancelled
	now := time.Now()
	t.CompletedAt = &now

	c.logger.Info("tournament cancelled", "tournament_id", tournamentID)
	return nil
}

// GetTournament returns a tournament by ID, or nil if not found.
func (c *Coordinator) GetTournament(tournamentID string) *Tournament {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tournaments[tournamentID]
}

// LaggingCandidates returns the task run IDs of candidates that have not
// yet completed. These can be terminated by the watchdog once judging begins.
func (c *Coordinator) LaggingCandidates(tournamentID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.tournaments[tournamentID]
	if !ok {
		return nil
	}
	return t.LaggingCandidateIDs()
}
