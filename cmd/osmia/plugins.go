package main

import (
	"fmt"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// claudePluginConfig converts engines.claude-code.plugins into the engine's
// plugin config and validates it, so a bad reference stops the controller at
// startup instead of failing every agent job.
func claudePluginConfig(cfg config.PluginsConfig) (claudecode.PluginConfig, error) {
	out := claudecode.PluginConfig{Sources: make([]claudecode.PluginSource, 0, len(cfg.Sources))}
	for _, s := range cfg.Sources {
		out.Sources = append(out.Sources, claudecode.PluginSource{
			Name:          s.Name,
			RepoURL:       s.RepoURL,
			Ref:           s.Ref,
			TokenSecret:   s.TokenSecret,
			TokenKey:      s.TokenKey,
			TokenUsername: s.TokenUsername,
		})
	}

	var err error
	if out.Default, err = parsePluginRefs("default", cfg.Default); err != nil {
		return claudecode.PluginConfig{}, err
	}
	if len(cfg.Profiles) > 0 {
		out.Profiles = make(map[string][]claudecode.PluginRef, len(cfg.Profiles))
	}
	for name, refs := range cfg.Profiles {
		if out.Profiles[name], err = parsePluginRefs("profiles."+name, refs); err != nil {
			return claudecode.PluginConfig{}, err
		}
	}

	if err := out.Validate(); err != nil {
		return claudecode.PluginConfig{}, err
	}
	return out, nil
}

// parsePluginRefs parses a list of "<source>/<plugin>" references.
func parsePluginRefs(field string, refs []string) ([]claudecode.PluginRef, error) {
	out := make([]claudecode.PluginRef, 0, len(refs))
	for _, s := range refs {
		r, err := claudecode.ParsePluginRef(s)
		if err != nil {
			return nil, fmt.Errorf("engines.claude-code.plugins.%s: %w", field, err)
		}
		out = append(out, r)
	}
	return out, nil
}
