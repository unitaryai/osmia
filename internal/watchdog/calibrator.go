// Package watchdog implements the progress watchdog loop that detects
// stalled, looping, or otherwise unproductive agents during execution.
//
// calibrator.go provides running percentile statistics per
// (repo_pattern, engine, task_type) combination, enabling the watchdog
// to calibrate thresholds from historical observations.
package watchdog

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// Signal identifies a telemetry signal tracked by the calibrator.
type Signal string

const (
	// SignalTokenRate tracks tokens consumed per minute.
	SignalTokenRate Signal = "token_rate"
	// SignalToolCallFrequency tracks tool calls per minute.
	SignalToolCallFrequency Signal = "tool_call_frequency"
	// SignalFileChangeRate tracks file changes per minute.
	SignalFileChangeRate Signal = "file_change_rate"
	// SignalCostVelocity tracks cost in USD per 10 minutes.
	SignalCostVelocity Signal = "cost_velocity"
	// SignalTotalDuration tracks total task duration in seconds.
	SignalTotalDuration Signal = "total_duration"
	// SignalConsecutiveIdenticalCalls tracks the peak consecutive identical
	// tool calls observed before resolution.
	SignalConsecutiveIdenticalCalls Signal = "consecutive_identical_calls"
)

// AllSignals is a convenience slice of every defined signal, useful for
// iterating over all signals when recording or querying.
var AllSignals = []Signal{
	SignalTokenRate,
	SignalToolCallFrequency,
	SignalFileChangeRate,
	SignalCostVelocity,
	SignalTotalDuration,
	SignalConsecutiveIdenticalCalls,
}

// Observation represents a single data point collected after a TaskRun
// completes, used to feed the calibrator.
type Observation struct {
	RepoURL              string
	Engine               string
	TaskType             string
	TokensConsumed       int64
	ToolCallsTotal       int
	FilesChanged         int
	CostEstimateUSD      float64
	DurationSeconds      float64
	ConsecutiveIdentical int
	CompletedAt          time.Time
}

// Percentiles holds computed percentile values for a given signal.
type Percentiles struct {
	P50         float64 `json:"p50"`
	P90         float64 `json:"p90"`
	P99         float64 `json:"p99"`
	SampleCount int     `json:"sample_count"`
}

// Calibrator tracks per-(repo_pattern, engine, task_type) statistics for
// key telemetry signals and computes running percentiles. It is safe for
// concurrent use.
type Calibrator struct {
	mu     sync.RWMutex
	data   map[ProfileKey]*signalData
	logger *slog.Logger
}

// signalData holds the sorted sample lists for each signal belonging to
// a single profile key.
type signalData struct {
	samples   map[Signal][]float64
	updatedAt time.Time
}

// NewCalibrator creates a new Calibrator with the given logger.
func NewCalibrator(logger *slog.Logger) *Calibrator {
	return &Calibrator{
		data:   make(map[ProfileKey]*signalData),
		logger: logger,
	}
}

// Record adds a data point from a completed TaskRun. The observation is
// decomposed into individual signal values and stored per profile key.
func (c *Calibrator) Record(_ context.Context, obs Observation) {
	key := ProfileKey{
		RepoPattern: obs.RepoURL,
		Engine:      obs.Engine,
		TaskType:    obs.TaskType,
	}

	durationMin := obs.DurationSeconds / 60.0
	if durationMin <= 0 {
		durationMin = 1.0 // avoid division by zero
	}

	values := map[Signal]float64{
		SignalTokenRate:                 float64(obs.TokensConsumed) / durationMin,
		SignalToolCallFrequency:         float64(obs.ToolCallsTotal) / durationMin,
		SignalFileChangeRate:            float64(obs.FilesChanged) / durationMin,
		SignalCostVelocity:              obs.CostEstimateUSD / durationMin * 10.0,
		SignalTotalDuration:             obs.DurationSeconds,
		SignalConsecutiveIdenticalCalls: float64(obs.ConsecutiveIdentical),
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sd, ok := c.data[key]
	if !ok {
		sd = &signalData{
			samples: make(map[Signal][]float64),
		}
		c.data[key] = sd
	}
	sd.updatedAt = obs.CompletedAt

	for sig, val := range values {
		sd.samples[sig] = insertSorted(sd.samples[sig], val)
	}

	c.logger.Debug("calibration observation recorded",
		"repo", obs.RepoURL,
		"engine", obs.Engine,
		"task_type", obs.TaskType,
		"sample_count", len(sd.samples[SignalTokenRate]),
	)
}

// GetPercentiles returns the P50, P90, P99 percentiles for the given
// profile key and signal. Returns nil if no samples exist for that key.
func (c *Calibrator) GetPercentiles(_ context.Context, key ProfileKey, sig Signal) *Percentiles {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sd, ok := c.data[key]
	if !ok {
		return nil
	}

	samples := sd.samples[sig]
	if len(samples) == 0 {
		return nil
	}

	return &Percentiles{
		P50:         percentile(samples, 0.50),
		P90:         percentile(samples, 0.90),
		P99:         percentile(samples, 0.99),
		SampleCount: len(samples),
	}
}

// SampleCount returns the number of observations recorded for a given key.
func (c *Calibrator) SampleCount(key ProfileKey) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sd, ok := c.data[key]
	if !ok {
		return 0
	}

	// All signals have the same count; pick any.
	for _, s := range sd.samples {
		return len(s)
	}
	return 0
}

// AllKeys returns all profile keys that have recorded observations.
func (c *Calibrator) AllKeys() []ProfileKey {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]ProfileKey, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	return keys
}

// insertSorted inserts val into an already-sorted slice, maintaining
// ascending order. This is a simple O(n) approach suitable for V1
// where sample counts are moderate.
func insertSorted(sorted []float64, val float64) []float64 {
	i := sort.SearchFloat64s(sorted, val)
	sorted = append(sorted, 0)
	copy(sorted[i+1:], sorted[i:])
	sorted[i] = val
	return sorted
}

// percentile returns the p-th percentile (0.0–1.0) from a sorted slice
// using linear interpolation.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}

	rank := p * float64(n-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}

	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
