package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSignature() Signature {
	return Signature{
		Name:        "ScoreStep",
		Description: "Score an agent tool call step",
		InputFields: []Field{
			{Name: "tool_calls", Description: "recent tool calls", Type: FieldTypeString, Required: true},
			{Name: "context", Description: "optional context", Type: FieldTypeString, Required: false},
		},
		OutputFields: []Field{
			{Name: "score", Description: "quality score 1-10", Type: FieldTypeInt, Required: true},
			{Name: "reasoning", Description: "explanation", Type: FieldTypeString, Required: true},
		},
	}
}

func TestFormatPrompt(t *testing.T) {
	sig := testSignature()
	inputs := map[string]any{
		"tool_calls": "read file.go, edit file.go",
		"context":    "fixing a bug",
	}

	sys, usr, err := FormatPrompt(sig, inputs)
	require.NoError(t, err)

	// System prompt should contain the signature name and output format.
	assert.Contains(t, sys, "ScoreStep")
	assert.Contains(t, sys, "score")
	assert.Contains(t, sys, "reasoning")
	assert.Contains(t, sys, "JSON")

	// User message should contain the input values.
	assert.Contains(t, usr, "read file.go, edit file.go")
	assert.Contains(t, usr, "fixing a bug")
}

func TestFormatPromptMissingRequired(t *testing.T) {
	sig := testSignature()
	inputs := map[string]any{
		"context": "only optional provided",
	}

	_, _, err := FormatPrompt(sig, inputs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tool_calls")
}

func TestFormatPromptInvalidSignature(t *testing.T) {
	sig := Signature{Name: ""} // invalid
	_, _, err := FormatPrompt(sig, nil)
	assert.Error(t, err)
}

func TestParseResponse(t *testing.T) {
	sig := testSignature()

	tests := []struct {
		name        string
		response    string
		expectScore int
		expectErr   bool
	}{
		{
			name:        "valid json",
			response:    `{"score": 8, "reasoning": "good approach"}`,
			expectScore: 8,
		},
		{
			name:        "json with markdown fences",
			response:    "```json\n{\"score\": 7, \"reasoning\": \"decent\"}\n```",
			expectScore: 7,
		},
		{
			name:      "not json",
			response:  "The score is 5",
			expectErr: true,
		},
		{
			name:      "missing required field",
			response:  `{"score": 5}`,
			expectErr: true,
		},
		{
			name:        "float score coerced to int",
			response:    `{"score": 9.0, "reasoning": "excellent"}`,
			expectScore: 9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseResponse(sig, tt.response)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectScore, result["score"])
			assert.NotEmpty(t, result["reasoning"])
		})
	}
}

func TestConvertFieldTypes(t *testing.T) {
	tests := []struct {
		name      string
		field     Field
		value     any
		expected  any
		expectErr bool
	}{
		{
			name:     "string from string",
			field:    Field{Name: "s", Type: FieldTypeString},
			value:    "hello",
			expected: "hello",
		},
		{
			name:     "string from number",
			field:    Field{Name: "s", Type: FieldTypeString},
			value:    42.0,
			expected: "42",
		},
		{
			name:     "int from float64",
			field:    Field{Name: "n", Type: FieldTypeInt},
			value:    7.0,
			expected: 7,
		},
		{
			name:     "int from string",
			field:    Field{Name: "n", Type: FieldTypeInt},
			value:    "42",
			expected: 42,
		},
		{
			name:      "int from invalid string",
			field:     Field{Name: "n", Type: FieldTypeInt},
			value:     "abc",
			expectErr: true,
		},
		{
			name:     "float from float64",
			field:    Field{Name: "f", Type: FieldTypeFloat},
			value:    3.14,
			expected: 3.14,
		},
		{
			name:     "bool from bool",
			field:    Field{Name: "b", Type: FieldTypeBool},
			value:    true,
			expected: true,
		},
		{
			name:     "bool from string",
			field:    Field{Name: "b", Type: FieldTypeBool},
			value:    "true",
			expected: true,
		},
		{
			name:     "json passthrough",
			field:    Field{Name: "j", Type: FieldTypeJSON},
			value:    map[string]any{"key": "val"},
			expected: map[string]any{"key": "val"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertField(tt.field, tt.value)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
	assert.Equal(t, "", truncate("", 5))
}
