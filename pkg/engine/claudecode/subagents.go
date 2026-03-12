// Package claudecode provides the Claude Code execution engine for Osmia.
// This file implements sub-agent configuration for Claude Code, replacing the
// deprecated agent teams feature with the official sub-agents specification.
package claudecode

import (
	"encoding/json"
	"fmt"

	"github.com/unitaryai/osmia/pkg/engine"
)

// SubAgent describes a Claude Code sub-agent definition.
type SubAgent struct {
	// Name is the sub-agent identifier.
	Name string
	// Description is a short summary of what this sub-agent does.
	Description string
	// Prompt is the inline system prompt.
	Prompt string
	// Model selects the model: "sonnet", "opus", "haiku", or "inherit".
	Model string
	// Tools is an allowlist of tools the sub-agent may use.
	Tools []string
	// DisallowedTools is a denylist of tools the sub-agent must not use.
	DisallowedTools []string
	// PermissionMode controls the sub-agent's permission behaviour.
	PermissionMode string
	// MaxTurns limits the number of agentic turns.
	MaxTurns int
	// Skills lists skill names to preload.
	Skills []string
	// Background runs the sub-agent as a background process.
	Background bool
	// ConfigMap loads the sub-agent definition from a Kubernetes ConfigMap.
	ConfigMap string
	// Key is the key within the ConfigMap (defaults to "<name>.md").
	Key string
}

// SubAgentFlag builds the --agents CLI flag for inline sub-agents in the
// official Claude Code format: {"name": {"description":"...", ...}}.
// ConfigMap-backed sub-agents are excluded (they are loaded as files).
func SubAgentFlag(agents []SubAgent) ([]string, error) {
	inline := filterInlineAgents(agents)
	if len(inline) == 0 {
		return nil, nil
	}

	m := make(map[string]any, len(inline))
	for _, a := range inline {
		entry := map[string]any{
			"description": a.Description,
		}
		if a.Prompt != "" {
			entry["prompt"] = a.Prompt
		}
		if a.Model != "" {
			entry["model"] = a.Model
		}
		if len(a.Tools) > 0 {
			entry["tools"] = a.Tools
		}
		if len(a.DisallowedTools) > 0 {
			entry["disallowedTools"] = a.DisallowedTools
		}
		if a.PermissionMode != "" {
			entry["permissionMode"] = a.PermissionMode
		}
		if a.MaxTurns > 0 {
			entry["maxTurns"] = a.MaxTurns
		}
		if len(a.Skills) > 0 {
			entry["skills"] = a.Skills
		}
		if a.Background {
			entry["background"] = true
		}
		m[a.Name] = entry
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshalling sub-agent definitions: %w", err)
	}
	return []string{"--agents", string(data)}, nil
}

// SubAgentEnvVars returns environment variables for ConfigMap-backed sub-agents.
// setup-claude.sh reads CLAUDE_SUBAGENT_PATH_<NAME> and copies the file to
// ~/.claude/agents/.
func SubAgentEnvVars(agents []SubAgent) map[string]string {
	if len(agents) == 0 {
		return nil
	}
	env := make(map[string]string)
	for _, a := range agents {
		if a.ConfigMap == "" {
			continue
		}
		safe := toSafeEnvName(a.Name)
		env["CLAUDE_SUBAGENT_PATH_"+safe] = "/subagents/" + a.Name + ".md"
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// SubAgentVolumes returns ConfigMap volume mounts for ConfigMap-backed
// sub-agents. Each gets a dedicated mount at /subagents/<name>.md.
func SubAgentVolumes(agents []SubAgent) []engine.VolumeMount {
	var mounts []engine.VolumeMount
	for _, a := range agents {
		if a.ConfigMap == "" {
			continue
		}
		key := a.Key
		if key == "" {
			key = a.Name + ".md"
		}
		mounts = append(mounts, engine.VolumeMount{
			Name:          "subagent-" + toSafeVolumeName(a.Name),
			MountPath:     "/subagents/" + a.Name + ".md",
			SubPath:       key,
			ReadOnly:      true,
			ConfigMapName: a.ConfigMap,
			ConfigMapKey:  key,
		})
	}
	return mounts
}

// filterInlineAgents returns only sub-agents without a ConfigMap (i.e. inline).
func filterInlineAgents(agents []SubAgent) []SubAgent {
	var result []SubAgent
	for _, a := range agents {
		if a.ConfigMap == "" {
			result = append(result, a)
		}
	}
	return result
}
