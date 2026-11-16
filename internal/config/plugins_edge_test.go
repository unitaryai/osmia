package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestPluginProfileEdgeForms checks profile values that are easy to write by
// accident: a null or empty entry is an empty profile, not an error.
func TestPluginProfileEdgeForms(t *testing.T) {
	var got PluginsConfig
	require.NoError(t, yaml.Unmarshal([]byte("profiles:\n  a: null\n  b: {}\n  c: []\n"), &got))
	assert.Equal(t, PluginProfileConfig{}, got.Profiles["a"])
	assert.Equal(t, PluginProfileConfig{}, got.Profiles["b"])
	assert.Empty(t, got.Profiles["c"].Plugins)
}
