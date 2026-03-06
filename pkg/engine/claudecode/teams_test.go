package claudecode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTeamsEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TeamsConfig
		wantNil bool
		check   func(t *testing.T, env map[string]string)
	}{
		{
			name: "disabled returns nil",
			cfg: TeamsConfig{
				Enabled: false,
			},
			wantNil: true,
		},
		{
			name: "enabled sets experimental flag",
			cfg: TeamsConfig{
				Enabled: true,
			},
			check: func(t *testing.T, env map[string]string) {
				assert.Equal(t, "1", env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"])
			},
		},
		{
			name: "enabled with max teammates sets both vars",
			cfg: TeamsConfig{
				Enabled:      true,
				MaxTeammates: 5,
			},
			check: func(t *testing.T, env map[string]string) {
				assert.Equal(t, "1", env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"])
				assert.Equal(t, "5", env["CLAUDE_CODE_MAX_TEAMMATES"])
			},
		},
		{
			name: "zero max teammates omits max teammates var",
			cfg: TeamsConfig{
				Enabled:      true,
				MaxTeammates: 0,
			},
			check: func(t *testing.T, env map[string]string) {
				assert.Equal(t, "1", env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"])
				_, ok := env["CLAUDE_CODE_MAX_TEAMMATES"]
				assert.False(t, ok, "CLAUDE_CODE_MAX_TEAMMATES should not be set when zero")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := TeamsEnvVars(tt.cfg)

			if tt.wantNil {
				assert.Nil(t, env)
				return
			}

			require.NotNil(t, env)
			if tt.check != nil {
				tt.check(t, env)
			}
		})
	}
}

func TestTeamsFlags(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TeamsConfig
		wantNil bool
		want    []string
	}{
		{
			name: "disabled returns nil",
			cfg: TeamsConfig{
				Enabled: false,
			},
			wantNil: true,
		},
		{
			name: "enabled with in-process mode",
			cfg: TeamsConfig{
				Enabled: true,
				Mode:    "in-process",
			},
			want: []string{"--teammate-mode", "in-process"},
		},
		{
			name: "enabled with empty mode defaults to in-process",
			cfg: TeamsConfig{
				Enabled: true,
			},
			want: []string{"--teammate-mode", "in-process"},
		},
		{
			name: "enabled with tmux mode",
			cfg: TeamsConfig{
				Enabled: true,
				Mode:    "tmux",
			},
			want: []string{"--teammate-mode", "tmux"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := TeamsFlags(tt.cfg)

			if tt.wantNil {
				assert.Nil(t, flags)
				return
			}

			assert.Equal(t, tt.want, flags)
		})
	}
}

func TestDefaultTeamsConfig(t *testing.T) {
	cfg := DefaultTeamsConfig()

	assert.False(t, cfg.Enabled)
	assert.Equal(t, "in-process", cfg.Mode)
	assert.Equal(t, 3, cfg.MaxTeammates)
}
