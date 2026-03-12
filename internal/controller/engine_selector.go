// Package controller implements the main reconciliation loop for the Osmia
// operator. This file contains the engine selection logic including fallback
// chain support.
package controller

import (
	"strings"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

const engineLabelPrefix = "osmia:engine:"

// EngineSelector determines the ordered list of engines to use for a ticket.
type EngineSelector interface {
	// SelectEngines returns an ordered list of engine names to attempt for
	// the given ticket. The first engine is tried first; subsequent engines
	// are fallbacks if the preceding engine fails.
	SelectEngines(ticket ticketing.Ticket) []string
}

// DefaultEngineSelector selects engines based on ticket labels and the
// configured default/fallback chain.
type DefaultEngineSelector struct {
	cfg     *config.Config
	engines map[string]engine.ExecutionEngine
}

// NewDefaultEngineSelector creates a DefaultEngineSelector with the given
// configuration and registered engine map.
func NewDefaultEngineSelector(cfg *config.Config, engines map[string]engine.ExecutionEngine) *DefaultEngineSelector {
	return &DefaultEngineSelector{
		cfg:     cfg,
		engines: engines,
	}
}

// SelectEngines returns the ordered engine list for a ticket. If the ticket
// carries a label of the form "osmia:engine:<name>" and that engine is
// registered, it is used exclusively (no fallback). Otherwise the default
// engine followed by the configured fallback engines is returned, with any
// unregistered engines filtered out.
func (s *DefaultEngineSelector) SelectEngines(ticket ticketing.Ticket) []string {
	// Check for a label override.
	for _, label := range ticket.Labels {
		if strings.HasPrefix(label, engineLabelPrefix) {
			name := strings.TrimPrefix(label, engineLabelPrefix)
			if _, ok := s.engines[name]; ok {
				return []string{name}
			}
		}
	}

	// Build the chain: [default] + fallback_engines, filtered to registered engines.
	candidates := make([]string, 0, 1+len(s.cfg.Engines.FallbackEngines))

	defaultEngine := s.cfg.Engines.Default
	if defaultEngine == "" {
		defaultEngine = "claude-code"
	}
	candidates = append(candidates, defaultEngine)
	candidates = append(candidates, s.cfg.Engines.FallbackEngines...)

	result := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if _, ok := s.engines[name]; ok {
			result = append(result, name)
		}
	}

	return result
}
