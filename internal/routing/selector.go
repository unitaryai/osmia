package routing

import (
	"context"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// FallbackSelector is implemented by the default engine selector in the
// controller package and used as a fallback when insufficient fingerprint
// data is available.
type FallbackSelector interface {
	SelectEngines(ticket ticketing.Ticket) []string
}

// IntelligentSelector selects engines based on historical fingerprint data,
// using epsilon-greedy exploration to balance exploitation of the best-known
// engine with discovery of new engine capabilities.
type IntelligentSelector struct {
	store            FingerprintStore
	fallback         FallbackSelector
	cfg              *config.RoutingConfig
	logger           *slog.Logger
	rng              *rand.Rand
	availableEngines []string
}

// NewIntelligentSelector creates a selector that routes tasks using
// fingerprint data, falling back to the provided selector when data is
// insufficient.
func NewIntelligentSelector(
	store FingerprintStore,
	fallback FallbackSelector,
	cfg *config.RoutingConfig,
	availableEngines []string,
	logger *slog.Logger,
) *IntelligentSelector {
	return &IntelligentSelector{
		store:            store,
		fallback:         fallback,
		cfg:              cfg,
		logger:           logger,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		availableEngines: availableEngines,
	}
}

// engineScore pairs an engine name with its computed fitness score.
type engineScore struct {
	name  string
	score float64
}

// SelectEngines returns an ordered list of engines for the given ticket.
// When sufficient fingerprint data exists, engines are ranked by their
// composite score. With probability epsilon, a random engine is promoted
// to first position (exploration). If fingerprint data is insufficient
// the fallback selector is used instead.
func (s *IntelligentSelector) SelectEngines(ticket ticketing.Ticket) []string {
	ctx := context.Background()

	query := queryFromTicket(ticket)

	// Collect fingerprints for all available engines.
	scores := make([]engineScore, 0, len(s.availableEngines))
	hasSufficientData := false
	minSamples := s.cfg.MinSamplesForRouting
	if minSamples <= 0 {
		minSamples = 5
	}

	for _, name := range s.availableEngines {
		fp, err := s.store.Get(ctx, name)
		if err != nil {
			// No fingerprint — use uninformed prior.
			scores = append(scores, engineScore{name: name, score: defaultScore()})
			continue
		}

		if fp.TotalTasks >= minSamples {
			hasSufficientData = true
		}

		scores = append(scores, engineScore{name: name, score: fp.Score(query)})
	}

	// Fall back to static selector when we lack data.
	if !hasSufficientData {
		s.logger.Debug("insufficient fingerprint data, using fallback selector",
			"min_samples", minSamples,
		)
		if s.fallback == nil {
			// No fallback configured — return available engines in their
			// current (arbitrary) order as a best-effort response.
			result := make([]string, len(s.availableEngines))
			copy(result, s.availableEngines)
			return result
		}
		return s.fallback.SelectEngines(ticket)
	}

	// Sort by score descending.
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Epsilon-greedy exploration: with probability epsilon, swap a random
	// engine into the top position.
	if s.rng.Float64() < s.cfg.EpsilonGreedy && len(scores) > 1 {
		idx := s.rng.Intn(len(scores)-1) + 1 // pick anything except index 0
		scores[0], scores[idx] = scores[idx], scores[0]

		metrics.RoutingExplorationTotal.Inc()

		s.logger.Info("epsilon-greedy exploration triggered",
			"selected", scores[0].name,
			"epsilon", s.cfg.EpsilonGreedy,
		)
	}

	result := make([]string, len(scores))
	for i, es := range scores {
		result[i] = es.name
	}

	if len(result) > 0 {
		metrics.RoutingEngineSelectedTotal.WithLabelValues(result[0]).Inc()
	}

	s.logger.Info("intelligent routing completed",
		"selected", result,
		"top_score", scores[0].score,
	)

	return result
}

// SetFallback updates the fallback selector used when insufficient fingerprint
// data is available. This allows the fallback to be set after construction,
// which is necessary when the default engine selector is built inside the
// reconciler constructor.
func (s *IntelligentSelector) SetFallback(fb FallbackSelector) {
	s.fallback = fb
}

// RecordOutcome updates the fingerprint store with a completed task outcome.
func (s *IntelligentSelector) RecordOutcome(ctx context.Context, outcome TaskOutcome) error {
	fp, err := s.store.Get(ctx, outcome.EngineName)
	if err != nil {
		fp = NewEngineFingerprint(outcome.EngineName)
	}

	fp.Update(outcome)

	// Update metrics gauges.
	for dim, ds := range fp.Dimensions {
		for val, vs := range ds.Values {
			metrics.RoutingSuccessRate.WithLabelValues(outcome.EngineName, dim, val).Set(vs.SuccessRate())
		}
	}
	metrics.RoutingFingerprintSamples.WithLabelValues(outcome.EngineName).Set(float64(fp.TotalTasks))

	return s.store.Save(ctx, fp)
}

// defaultScore returns the uninformed prior score (0.5^4 for 4 dimensions).
func defaultScore() float64 {
	score := 1.0
	for range AllDimensions() {
		score *= 0.5
	}
	return score
}

// queryFromTicket extracts routing dimensions from a ticket.
func queryFromTicket(ticket ticketing.Ticket) RoutingQuery {
	return RoutingQuery{
		TaskType:     ticket.TicketType,
		RepoLanguage: extractLanguageLabel(ticket.Labels),
		Complexity:   extractComplexityLabel(ticket.Labels),
	}
}

// extractLanguageLabel scans ticket labels for a "lang:" prefixed value.
func extractLanguageLabel(labels []string) string {
	for _, l := range labels {
		if len(l) > 5 && l[:5] == "lang:" {
			return l[5:]
		}
	}
	return ""
}

// extractComplexityLabel scans ticket labels for a "complexity:" prefixed value.
func extractComplexityLabel(labels []string) string {
	for _, l := range labels {
		if len(l) > 11 && l[:11] == "complexity:" {
			return l[11:]
		}
	}
	return ""
}
