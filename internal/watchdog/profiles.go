package watchdog

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// ProfileKey identifies the combination of repository pattern, engine, and
// task type for which calibrated thresholds are tracked.
type ProfileKey struct {
	RepoPattern string `json:"repo_pattern"`
	Engine      string `json:"engine"`
	TaskType    string `json:"task_type"`
}

// CalibratedProfile holds the calibrated percentile thresholds for a
// specific profile key, along with metadata about when it was last updated.
type CalibratedProfile struct {
	Key         ProfileKey              `json:"key"`
	Thresholds  map[Signal]*Percentiles `json:"thresholds"`
	LastUpdated time.Time               `json:"last_updated"`
	SampleCount int                     `json:"sample_count"`
}

// ProfileStore is the interface for retrieving calibrated profiles.
// Implementations may be backed by an in-memory cache, a database, or
// other persistent stores.
type ProfileStore interface {
	// Get retrieves the calibrated profile for the exact key, or nil if
	// no profile exists.
	Get(ctx context.Context, key ProfileKey) *CalibratedProfile

	// Put stores or updates a calibrated profile.
	Put(ctx context.Context, profile *CalibratedProfile)

	// List returns all stored profiles.
	List(ctx context.Context) []*CalibratedProfile
}

// MemoryProfileStore is a thread-safe in-memory implementation of ProfileStore.
type MemoryProfileStore struct {
	mu       sync.RWMutex
	profiles map[ProfileKey]*CalibratedProfile
}

// NewMemoryProfileStore creates a new MemoryProfileStore.
func NewMemoryProfileStore() *MemoryProfileStore {
	return &MemoryProfileStore{
		profiles: make(map[ProfileKey]*CalibratedProfile),
	}
}

// Get retrieves the calibrated profile for the exact key.
func (m *MemoryProfileStore) Get(_ context.Context, key ProfileKey) *CalibratedProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.profiles[key]
}

// Put stores or updates a calibrated profile.
func (m *MemoryProfileStore) Put(_ context.Context, profile *CalibratedProfile) {
	if profile == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profiles[profile.Key] = profile
}

// List returns all stored profiles.
func (m *MemoryProfileStore) List(_ context.Context) []*CalibratedProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*CalibratedProfile, 0, len(m.profiles))
	for _, p := range m.profiles {
		result = append(result, p)
	}
	return result
}

// ProfileResolver resolves the best matching calibrated profile for a
// given (repoURL, engine, taskType) combination. The resolution strategy
// is: exact match > partial match (engine+taskType) > global fallback >
// static defaults (nil).
type ProfileResolver struct {
	store      ProfileStore
	calibrator *Calibrator
	minSamples int
}

// NewProfileResolver creates a new ProfileResolver.
func NewProfileResolver(store ProfileStore, calibrator *Calibrator, minSamples int) *ProfileResolver {
	if minSamples <= 0 {
		minSamples = 10
	}
	return &ProfileResolver{
		store:      store,
		calibrator: calibrator,
		minSamples: minSamples,
	}
}

// ResolveProfile finds the best matching calibrated profile. It tries
// exact match first, then partial (matching engine and task type with
// any repo), then a global fallback. Returns nil when no profile has
// enough samples to be trusted (cold-start scenario).
func (r *ProfileResolver) ResolveProfile(ctx context.Context, repoURL, engineName, taskType string) *CalibratedProfile {
	// 1. Exact match.
	exactKey := ProfileKey{RepoPattern: repoURL, Engine: engineName, TaskType: taskType}
	if p := r.tryResolve(ctx, exactKey); p != nil {
		return p
	}

	// 2. Glob-based repo match. Check all stored profiles for a repo
	//    pattern that matches the given repoURL.
	for _, p := range r.store.List(ctx) {
		if p.Key.Engine == engineName && p.Key.TaskType == taskType &&
			p.Key.RepoPattern != repoURL && matchRepoGlob(p.Key.RepoPattern, repoURL) &&
			p.SampleCount >= r.minSamples {
			return p
		}
	}

	// 3. Partial match: engine + task type, any repo.
	partialKey := ProfileKey{RepoPattern: "*", Engine: engineName, TaskType: taskType}
	if p := r.tryResolve(ctx, partialKey); p != nil {
		return p
	}

	// 4. Global fallback: any repo, any engine, same task type.
	globalKey := ProfileKey{RepoPattern: "*", Engine: "*", TaskType: taskType}
	if p := r.tryResolve(ctx, globalKey); p != nil {
		return p
	}

	// 5. No calibrated data available — caller should use static defaults.
	return nil
}

// tryResolve attempts to load a profile from the store and checks
// whether it meets the minimum sample requirement.
func (r *ProfileResolver) tryResolve(ctx context.Context, key ProfileKey) *CalibratedProfile {
	p := r.store.Get(ctx, key)
	if p != nil && p.SampleCount >= r.minSamples {
		return p
	}
	return nil
}

// RefreshProfile computes an up-to-date CalibratedProfile from the
// calibrator's raw samples and stores it in the profile store.
func (r *ProfileResolver) RefreshProfile(ctx context.Context, key ProfileKey) *CalibratedProfile {
	count := r.calibrator.SampleCount(key)
	if count == 0 {
		return nil
	}

	thresholds := make(map[Signal]*Percentiles)
	for _, sig := range AllSignals {
		if p := r.calibrator.GetPercentiles(ctx, key, sig); p != nil {
			thresholds[sig] = p
		}
	}

	profile := &CalibratedProfile{
		Key:         key,
		Thresholds:  thresholds,
		LastUpdated: time.Now(),
		SampleCount: count,
	}

	r.store.Put(ctx, profile)
	return profile
}

// matchRepoGlob performs glob matching on a repository URL pattern.
// It supports standard filepath.Match patterns such as * and ?.
func matchRepoGlob(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, value)
	if err != nil {
		return false
	}
	return matched
}
