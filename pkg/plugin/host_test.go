package plugin

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPlugin_VersionMismatch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name       string
		cfg        PluginConfig
		wantErr    bool
		errContain string
	}{
		{
			name: "SCM plugin with version 1 is rejected (controller expects 2)",
			cfg: PluginConfig{
				Name:             "old-scm",
				Type:             PluginTypeSCM,
				InterfaceVersion: 1,
			},
			wantErr:    true,
			errContain: "interface version 1 but controller expects 2",
		},
		{
			name: "SCM plugin with version 2 passes version check",
			cfg: PluginConfig{
				Name:             "good-scm",
				Type:             PluginTypeSCM,
				InterfaceVersion: 2,
			},
			wantErr: false,
		},
		{
			name: "ticketing plugin with version 1 passes version check",
			cfg: PluginConfig{
				Name:             "good-ticketing",
				Type:             PluginTypeTicketing,
				InterfaceVersion: 1,
			},
			wantErr: false,
		},
		{
			name: "ticketing plugin with version 2 is rejected (controller expects 1)",
			cfg: PluginConfig{
				Name:             "future-ticketing",
				Type:             PluginTypeTicketing,
				InterfaceVersion: 2,
			},
			wantErr:    true,
			errContain: "interface version 2 but controller expects 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInterfaceVersion(logger, tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoadPlugin_DuplicateRejected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	host := NewHost(DefaultHealthConfig(), logger)

	// Manually insert a plugin to test duplicate detection.
	host.plugins["existing"] = &pluginInstance{
		config:  PluginConfig{Name: "existing"},
		healthy: true,
	}

	err := host.LoadPlugin(PluginConfig{
		Name:             "existing",
		Command:          "/bin/false",
		Type:             PluginTypeTicketing,
		InterfaceVersion: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already loaded")
}

func TestKnownInterfaceVersions_AllTypesPresent(t *testing.T) {
	allTypes := []PluginType{
		PluginTypeTicketing,
		PluginTypeNotifications,
		PluginTypeApproval,
		PluginTypeSecrets,
		PluginTypeReview,
		PluginTypeSCM,
	}

	for _, pt := range allTypes {
		_, ok := knownInterfaceVersions[pt]
		assert.True(t, ok, "knownInterfaceVersions should contain %s", pt)
	}
}

func TestKnownInterfaceVersions_SCMIsTwo(t *testing.T) {
	assert.Equal(t, 2, knownInterfaceVersions[PluginTypeSCM])
}
