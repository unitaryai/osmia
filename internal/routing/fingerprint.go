package routing

import (
	"sync"
	"time"
)

// DimensionName identifies a routing dimension for fingerprinting.
const (
	DimensionTaskType     = "task_type"
	DimensionRepoLanguage = "repo_language"
	DimensionRepoSize     = "repo_size_bucket"
	DimensionComplexity   = "complexity_bucket"
)

// AllDimensions returns the full list of routing dimensions.
func AllDimensions() []string {
	return []string{
		DimensionTaskType,
		DimensionRepoLanguage,
		DimensionRepoSize,
		DimensionComplexity,
	}
}

// ValueStats tracks success/failure counts for a single value within a
// dimension (e.g. "bug_fix" within the "task_type" dimension).
type ValueStats struct {
	Successes int `json:"successes"`
	Failures  int `json:"failures"`
}

// SuccessRate returns the Laplace-smoothed success rate: (s+1)/(s+f+2).
// This ensures new values start at 0.5 rather than 0, avoiding cold-start
// bias while converging to the empirical rate with more data.
func (vs *ValueStats) SuccessRate() float64 {
	return float64(vs.Successes+1) / float64(vs.Successes+vs.Failures+2)
}

// DimensionStats tracks per-value statistics for a single routing dimension.
type DimensionStats struct {
	Successes int                    `json:"successes"`
	Failures  int                    `json:"failures"`
	Values    map[string]*ValueStats `json:"values"`
}

// SuccessRate returns the Laplace-smoothed success rate for the entire
// dimension, aggregated across all values.
func (ds *DimensionStats) SuccessRate() float64 {
	return float64(ds.Successes+1) / float64(ds.Successes+ds.Failures+2)
}

// EngineFingerprint captures the historical performance profile of an engine
// across multiple task dimensions. It is safe for concurrent use.
type EngineFingerprint struct {
	mu          sync.RWMutex
	EngineName  string                     `json:"engine_name"`
	Dimensions  map[string]*DimensionStats `json:"dimensions"`
	LastUpdated time.Time                  `json:"last_updated"`
	TotalTasks  int                        `json:"total_tasks"`
}

// NewEngineFingerprint creates an empty fingerprint for the named engine,
// pre-initialising all known dimensions.
func NewEngineFingerprint(engineName string) *EngineFingerprint {
	dims := make(map[string]*DimensionStats, len(AllDimensions()))
	for _, d := range AllDimensions() {
		dims[d] = &DimensionStats{
			Values: make(map[string]*ValueStats),
		}
	}
	return &EngineFingerprint{
		EngineName: engineName,
		Dimensions: dims,
	}
}

// Update records a task outcome, incrementing the appropriate counters
// for each dimension.
func (ef *EngineFingerprint) Update(outcome TaskOutcome) {
	ef.mu.Lock()
	defer ef.mu.Unlock()

	ef.TotalTasks++
	ef.LastUpdated = time.Now()

	// Map outcome fields to dimension values.
	dimValues := map[string]string{
		DimensionTaskType:     outcome.TaskType,
		DimensionRepoLanguage: outcome.RepoLanguage,
		DimensionRepoSize:     repoSizeBucket(outcome.RepoSize),
		DimensionComplexity:   outcome.Complexity,
	}

	for dim, val := range dimValues {
		ds, ok := ef.Dimensions[dim]
		if !ok {
			ds = &DimensionStats{Values: make(map[string]*ValueStats)}
			ef.Dimensions[dim] = ds
		}

		if outcome.Success {
			ds.Successes++
		} else {
			ds.Failures++
		}

		if val == "" {
			continue
		}

		vs, ok := ds.Values[val]
		if !ok {
			vs = &ValueStats{}
			ds.Values[val] = vs
		}

		if outcome.Success {
			vs.Successes++
		} else {
			vs.Failures++
		}
	}
}

// Score computes a composite fitness score for the given routing query.
// For each dimension with a matching value, the per-value Laplace-smoothed
// success rate is used; otherwise the dimension-level rate is used. The
// overall score is the product of per-dimension rates, providing a
// natural penalty when any dimension is weak.
func (ef *EngineFingerprint) Score(query RoutingQuery) float64 {
	ef.mu.RLock()
	defer ef.mu.RUnlock()

	queryValues := map[string]string{
		DimensionTaskType:     query.TaskType,
		DimensionRepoLanguage: query.RepoLanguage,
		DimensionRepoSize:     repoSizeBucket(query.RepoSize),
		DimensionComplexity:   query.Complexity,
	}

	score := 1.0
	for dim, val := range queryValues {
		ds, ok := ef.Dimensions[dim]
		if !ok {
			// No data for this dimension — use uninformed prior (0.5).
			score *= 0.5
			continue
		}

		if val != "" {
			if vs, ok := ds.Values[val]; ok {
				score *= vs.SuccessRate()
				continue
			}
		}

		// Fall back to dimension-level rate.
		score *= ds.SuccessRate()
	}

	return score
}
