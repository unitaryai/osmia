package secretresolver

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// secretCommentPattern matches <!-- robodev:secrets ... --> HTML comment blocks.
var secretCommentPattern = regexp.MustCompile(`(?s)<!--\s*robodev:secrets\s*\n(.*?)-->`)

// labelPattern matches robodev:secret:ENV_NAME=URI label prefixes.
var labelPattern = regexp.MustCompile(`^robodev:secret:([A-Za-z_][A-Za-z0-9_]*)=(.+)$`)

// secretEntry represents a single entry inside a robodev:secrets YAML block.
type secretEntry struct {
	Ref   string `yaml:"ref"`
	Env   string `yaml:"env"`
	Alias string `yaml:"alias"`
}

// ParseCommentBlock extracts secret requests from <!-- robodev:secrets --> HTML
// comment blocks in a ticket description. The block contents are parsed as YAML.
func ParseCommentBlock(body string) ([]SecretRequest, error) {
	matches := secretCommentPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	var requests []SecretRequest
	for _, match := range matches {
		yamlContent := match[1]

		var entries []secretEntry
		if err := yaml.Unmarshal([]byte(yamlContent), &entries); err != nil {
			return nil, fmt.Errorf("parsing robodev:secrets YAML block: %w", err)
		}

		for _, entry := range entries {
			req, err := entryToRequest(entry)
			if err != nil {
				return nil, err
			}
			requests = append(requests, req)
		}
	}

	return requests, nil
}

// ParseLabels extracts secret requests from labels using the
// robodev:secret:ENV_NAME=URI format.
func ParseLabels(labels []string) ([]SecretRequest, error) {
	var requests []SecretRequest
	for _, label := range labels {
		matches := labelPattern.FindStringSubmatch(label)
		if matches == nil {
			continue
		}
		envName := matches[1]
		uri := matches[2]

		if envName == "" || uri == "" {
			return nil, fmt.Errorf("invalid secret label %q: env name and URI must be non-empty", label)
		}

		requests = append(requests, SecretRequest{
			EnvName: envName,
			URI:     uri,
		})
	}
	return requests, nil
}

// entryToRequest converts a parsed YAML entry into a SecretRequest.
func entryToRequest(entry secretEntry) (SecretRequest, error) {
	switch {
	case entry.Alias != "":
		alias := strings.TrimSpace(entry.Alias)
		if alias == "" {
			return SecretRequest{}, fmt.Errorf("empty alias in secret entry")
		}
		return SecretRequest{
			URI: "alias://" + alias,
		}, nil

	case entry.Ref != "":
		ref := strings.TrimSpace(entry.Ref)
		env := strings.TrimSpace(entry.Env)
		if ref == "" {
			return SecretRequest{}, fmt.Errorf("empty ref in secret entry")
		}
		if env == "" {
			return SecretRequest{}, fmt.Errorf("ref %q requires an env field", ref)
		}
		return SecretRequest{
			EnvName: env,
			URI:     ref,
		}, nil

	default:
		return SecretRequest{}, fmt.Errorf("secret entry must have either 'alias' or 'ref' field")
	}
}
