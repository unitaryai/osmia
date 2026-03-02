package llm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FormatPrompt converts a Signature and input values into a system prompt
// and user message suitable for an LLM API call. The output format is
// always structured JSON so responses can be parsed reliably.
func FormatPrompt(sig Signature, inputs map[string]any) (systemPrompt, userMessage string, err error) {
	if err := sig.Validate(); err != nil {
		return "", "", err
	}

	// Validate required inputs are present.
	for _, f := range sig.InputFields {
		if f.Required {
			if _, ok := inputs[f.Name]; !ok {
				return "", "", fmt.Errorf("missing required input field %q", f.Name)
			}
		}
	}

	// Build system prompt.
	var sys strings.Builder
	sys.WriteString(fmt.Sprintf("You are executing the %q operation.\n", sig.Name))
	if sig.Description != "" {
		sys.WriteString(sig.Description)
		sys.WriteString("\n\n")
	}

	sys.WriteString("## Output Format\n\n")
	sys.WriteString("Respond with a JSON object containing the following fields:\n\n")
	for _, f := range sig.OutputFields {
		required := ""
		if f.Required {
			required = " (required)"
		}
		sys.WriteString(fmt.Sprintf("- `%s` (%s)%s: %s\n", f.Name, f.Type, required, f.Description))
	}
	sys.WriteString("\nRespond ONLY with the JSON object. No markdown fences, no explanation.")

	// Build user message from inputs.
	var usr strings.Builder
	for _, f := range sig.InputFields {
		val, ok := inputs[f.Name]
		if !ok {
			continue
		}
		usr.WriteString(fmt.Sprintf("## %s\n\n", f.Name))
		if f.Description != "" {
			usr.WriteString(fmt.Sprintf("(%s)\n\n", f.Description))
		}
		usr.WriteString(fmt.Sprintf("%v\n\n", val))
	}

	return sys.String(), usr.String(), nil
}

// ParseResponse extracts typed output values from a raw LLM response text
// according to the signature's output fields. It expects the response to
// be a JSON object.
func ParseResponse(sig Signature, rawResponse string) (map[string]any, error) {
	// Strip markdown code fences if present.
	cleaned := strings.TrimSpace(rawResponse)
	if strings.HasPrefix(cleaned, "```") {
		lines := strings.Split(cleaned, "\n")
		// Remove first and last lines (fences).
		if len(lines) >= 3 {
			lines = lines[1 : len(lines)-1]
			cleaned = strings.Join(lines, "\n")
		}
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return nil, fmt.Errorf("response is not valid JSON: %w (raw: %s)", err, truncate(rawResponse, 200))
	}

	result := make(map[string]any, len(sig.OutputFields))
	for _, f := range sig.OutputFields {
		val, ok := raw[f.Name]
		if !ok {
			if f.Required {
				return nil, fmt.Errorf("required output field %q missing from response", f.Name)
			}
			continue
		}

		converted, err := convertField(f, val)
		if err != nil {
			return nil, fmt.Errorf("converting field %q: %w", f.Name, err)
		}
		result[f.Name] = converted
	}

	return result, nil
}

// convertField coerces a raw JSON value to the expected FieldType.
func convertField(f Field, val any) (any, error) {
	switch f.Type {
	case FieldTypeString:
		s, ok := val.(string)
		if !ok {
			return fmt.Sprintf("%v", val), nil
		}
		return s, nil

	case FieldTypeInt:
		switch v := val.(type) {
		case float64:
			return int(v), nil
		case string:
			i, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to int", v)
			}
			return i, nil
		default:
			return nil, fmt.Errorf("unexpected type %T for int field", val)
		}

	case FieldTypeFloat:
		switch v := val.(type) {
		case float64:
			return v, nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to float", v)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("unexpected type %T for float field", val)
		}

	case FieldTypeBool:
		switch v := val.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to bool", v)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("unexpected type %T for bool field", val)
		}

	case FieldTypeJSON:
		return val, nil

	default:
		return val, nil
	}
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
