package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

func TestClaudePluginConfig(t *testing.T) {
	source := config.PluginSourceConfig{
		Name:        "team",
		RepoURL:     "https://gitlab.example.com/team/plugins.git",
		Ref:         "0123456789abcdef0123456789abcdef01234567",
		TokenSecret: "plugin-token",
	}

	tests := []struct {
		name    string
		in      config.PluginsConfig
		want    claudecode.PluginConfig
		wantErr string
	}{
		{
			name: "converts sources, defaults and profiles",
			in: config.PluginsConfig{
				Sources:  []config.PluginSourceConfig{source},
				Default:  []string{"team/engineer"},
				Profiles: map[string][]string{"build": {"team/implementations"}},
			},
			want: claudecode.PluginConfig{
				Sources: []claudecode.PluginSource{{
					Name:        "team",
					RepoURL:     "https://gitlab.example.com/team/plugins.git",
					Ref:         "0123456789abcdef0123456789abcdef01234567",
					TokenSecret: "plugin-token",
				}},
				Default:  []claudecode.PluginRef{{Source: "team", Plugin: "engineer"}},
				Profiles: map[string][]claudecode.PluginRef{"build": {{Source: "team", Plugin: "implementations"}}},
			},
		},
		{
			name:    "a malformed reference names its field",
			in:      config.PluginsConfig{Sources: []config.PluginSourceConfig{source}, Profiles: map[string][]string{"build": {"implementations"}}},
			wantErr: "engines.claude-code.plugins.profiles.build",
		},
		{
			name:    "a plugin with no sources is rejected",
			in:      config.PluginsConfig{Default: []string{"team/engineer"}},
			wantErr: `unknown source "team"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claudePluginConfig(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
