package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hookEntry represents a single hook command within a matcher group.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// matcherGroup groups hooks under a tool matcher pattern.
type matcherGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// hooksConfig is the top-level structure for the Claude Code settings.json
// that configures guard rail hooks.
type hooksConfig struct {
	Hooks hooksSection `json:"hooks"`
}

// hooksSection contains the categorised hook lists.
type hooksSection struct {
	PreToolUse  []matcherGroup `json:"PreToolUse,omitempty"`
	PostToolUse []matcherGroup `json:"PostToolUse,omitempty"`
	Stop        []matcherGroup `json:"Stop,omitempty"`
}

// blockedCommandsScript generates a shell script that checks the tool input
// against a list of blocked commands and exits non-zero if a match is found.
func blockedCommandsScript(commands []string) string {
	if len(commands) == 0 {
		return "/opt/osmia/hooks/block-dangerous-commands.sh"
	}

	// Build a grep pattern from blocked commands.
	escaped := make([]string, len(commands))
	for i, cmd := range commands {
		escaped[i] = strings.ReplaceAll(cmd, "'", "'\\''")
	}

	return fmt.Sprintf(
		"/opt/osmia/hooks/block-dangerous-commands.sh '%s'",
		strings.Join(escaped, "|"),
	)
}

// blockedFilesScript generates a shell script that checks the tool input
// against a list of blocked file patterns and exits non-zero if a match is found.
func blockedFilesScript(patterns []string) string {
	if len(patterns) == 0 {
		return "/opt/osmia/hooks/block-sensitive-files.sh"
	}

	escaped := make([]string, len(patterns))
	for i, p := range patterns {
		escaped[i] = strings.ReplaceAll(p, "'", "'\\''")
	}

	return fmt.Sprintf(
		"/opt/osmia/hooks/block-sensitive-files.sh '%s'",
		strings.Join(escaped, "|"),
	)
}

// GenerateHooksConfig produces a JSON-encoded Claude Code settings.json
// containing guard rail hooks for:
//   - PreToolUse: blocking dangerous commands (Bash matcher) and sensitive
//     file writes (Write|Edit matcher)
//   - PostToolUse: heartbeat writer that updates /workspace/heartbeat.json
//   - Stop: completion handler that writes /workspace/result.json
//
// blockedCommands and blockedFilePatterns may be nil or empty; default
// hook scripts will still be referenced.
func GenerateHooksConfig(blockedCommands, blockedFilePatterns []string) ([]byte, error) {
	cfg := hooksConfig{
		Hooks: hooksSection{
			PreToolUse: []matcherGroup{
				{
					Matcher: "Bash",
					Hooks: []hookEntry{
						{
							Type:    "command",
							Command: blockedCommandsScript(blockedCommands),
						},
					},
				},
				{
					Matcher: "Write|Edit",
					Hooks: []hookEntry{
						{
							Type:    "command",
							Command: blockedFilesScript(blockedFilePatterns),
						},
					},
				},
			},
			PostToolUse: []matcherGroup{
				{
					Matcher: "Bash|Write|Edit|Read",
					Hooks: []hookEntry{
						{
							Type:    "command",
							Command: "/opt/osmia/hooks/heartbeat.sh",
						},
					},
				},
			},
			Stop: []matcherGroup{
				{
					Hooks: []hookEntry{
						{
							Type:    "command",
							Command: "/opt/osmia/hooks/on-complete.sh",
						},
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling hooks config: %w", err)
	}

	return data, nil
}
