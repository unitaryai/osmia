package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestPluginProfileForms checks that a plugin profile can still be written as
// a plain list of plugins, and also as a mapping that sets an engine image.
func TestPluginProfileForms(t *testing.T) {
	const doc = `
sources:
  - name: team
    repo_url: https://gitlab.example.com/team/plugins.git
    ref: 0123456789abcdef0123456789abcdef01234567
profiles:
  review: [team/reviewer]
  build:
    plugins: [team/builder]
    image: registry.example.com/engine-build:1.0
    init: /opt/tool/init.sh
    workdir: /opt/tool
`
	var got PluginsConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &got))

	assert.Equal(t, PluginProfileConfig{Plugins: []string{"team/reviewer"}}, got.Profiles["review"])
	assert.Equal(t, PluginProfileConfig{
		Plugins: []string{"team/builder"},
		Image:   "registry.example.com/engine-build:1.0",
		Init:    "/opt/tool/init.sh",
		Workdir: "/opt/tool",
	}, got.Profiles["build"])
}
