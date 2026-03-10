package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func TestDefaultEngineSelector_SelectEngines(t *testing.T) {
	engines := map[string]engine.ExecutionEngine{
		"claude-code": &mockEngine{name: "claude-code"},
		"cline":       &mockEngine{name: "cline"},
		"aider":       &mockEngine{name: "aider"},
	}

	tests := []struct {
		name     string
		cfg      *config.Config
		ticket   ticketing.Ticket
		expected []string
	}{
		{
			name: "default engine only when no fallbacks configured",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default: "claude-code",
				},
			},
			ticket:   ticketing.Ticket{ID: "T-1", Labels: []string{"osmia"}},
			expected: []string{"claude-code"},
		},
		{
			name: "default plus fallback chain",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"cline", "aider"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-2", Labels: []string{"osmia"}},
			expected: []string{"claude-code", "cline", "aider"},
		},
		{
			name: "label override returns single engine with no fallback",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"cline", "aider"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-3", Labels: []string{"osmia", "osmia:engine:aider"}},
			expected: []string{"aider"},
		},
		{
			name: "label override for unregistered engine is ignored",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"cline"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-4", Labels: []string{"osmia:engine:unknown"}},
			expected: []string{"claude-code", "cline"},
		},
		{
			name: "unregistered engines are filtered from fallback chain",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"nonexistent", "cline", "phantom"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-5", Labels: []string{}},
			expected: []string{"claude-code", "cline"},
		},
		{
			name: "empty default falls back to claude-code",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "",
					FallbackEngines: []string{"aider"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-6"},
			expected: []string{"claude-code", "aider"},
		},
		{
			name: "all fallback engines unregistered returns only default",
			cfg: &config.Config{
				Engines: config.EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"ghost", "phantom"},
				},
			},
			ticket:   ticketing.Ticket{ID: "T-7"},
			expected: []string{"claude-code"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := NewDefaultEngineSelector(tt.cfg, engines)
			result := selector.SelectEngines(tt.ticket)
			assert.Equal(t, tt.expected, result)
		})
	}
}
