package agentstream

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		input     string
		wantType  EventType
		wantErr   bool
		checkFunc func(t *testing.T, ev *StreamEvent)
	}{
		{
			name:     "tool_use event",
			input:    `{"type":"tool_use","tool":"Bash","args":{"command":"ls"},"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventToolCall,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				tc, ok := ev.Parsed.(*ToolCallEvent)
				require.True(t, ok, "Parsed should be *ToolCallEvent")
				assert.Equal(t, "Bash", tc.Tool)
				assert.JSONEq(t, `{"command":"ls"}`, string(tc.Args))
				assert.Equal(t, ts, ev.Timestamp)
			},
		},
		{
			name:     "content event",
			input:    `{"type":"content","content":"I'll check the files...","role":"assistant","timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventContentDelta,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				cd, ok := ev.Parsed.(*ContentDeltaEvent)
				require.True(t, ok, "Parsed should be *ContentDeltaEvent")
				assert.Equal(t, "I'll check the files...", cd.Content)
				assert.Equal(t, "assistant", cd.Role)
			},
		},
		{
			name:     "cost event",
			input:    `{"type":"cost","input_tokens":1500,"output_tokens":300,"cost_usd":0.012,"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventCost,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				ce, ok := ev.Parsed.(*CostEvent)
				require.True(t, ok, "Parsed should be *CostEvent")
				assert.Equal(t, 1500, ce.InputTokens)
				assert.Equal(t, 300, ce.OutputTokens)
				assert.InDelta(t, 0.012, ce.CostUSD, 0.0001)
			},
		},
		{
			name:     "result event",
			input:    `{"type":"result","success":true,"summary":"Fixed the bug","tests_passed":42,"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventResult,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				re, ok := ev.Parsed.(*ResultEvent)
				require.True(t, ok, "Parsed should be *ResultEvent")
				assert.True(t, re.Success)
				assert.Equal(t, "Fixed the bug", re.Summary)
				assert.Equal(t, 42, re.TestsPassed)
			},
		},
		{
			name:     "result event with merge request",
			input:    `{"type":"result","success":true,"summary":"Done","merge_request_url":"https://github.com/org/repo/pull/1","branch_name":"fix/bug","tests_passed":10,"tests_failed":0,"tests_added":3,"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventResult,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				re, ok := ev.Parsed.(*ResultEvent)
				require.True(t, ok)
				assert.Equal(t, "https://github.com/org/repo/pull/1", re.MergeRequestURL)
				assert.Equal(t, "fix/bug", re.BranchName)
				assert.Equal(t, 3, re.TestsAdded)
			},
		},
		{
			name:     "system event with session_id is parsed",
			input:    `{"type":"system","subtype":"init","session_id":"abc-123-xyz","timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventSystem,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				se, ok := ev.Parsed.(*SystemEvent)
				require.True(t, ok, "Parsed should be *SystemEvent")
				assert.Equal(t, "abc-123-xyz", se.SessionID)
				assert.Equal(t, "init", se.Subtype)
			},
		},
		{
			name:     "system event without session_id is parsed without error",
			input:    `{"type":"system","subtype":"error","timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventSystem,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				se, ok := ev.Parsed.(*SystemEvent)
				require.True(t, ok, "Parsed should be *SystemEvent")
				assert.Empty(t, se.SessionID)
				assert.Equal(t, "error", se.Subtype)
			},
		},
		{
			name:     "unknown event type preserved without error",
			input:    `{"type":"heartbeat","seq":5,"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventType("heartbeat"),
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				assert.Nil(t, ev.Parsed, "unknown types should have nil Parsed")
				assert.NotEmpty(t, ev.Raw, "Raw should still be populated")
			},
		},
		{
			name:    "empty line",
			input:   "",
			wantErr: true,
		},
		{
			name:    "malformed json",
			input:   `{not valid json}`,
			wantErr: true,
		},
		{
			name:    "missing type field",
			input:   `{"tool":"Bash","timestamp":"2026-01-01T00:00:00Z"}`,
			wantErr: true,
		},
		{
			name:     "result event with structured_output from --json-schema",
			input:    `{"type":"result","is_error":false,"result":"","structured_output":{"success":true,"summary":"Upgraded httpx","merge_request_url":"https://gitlab.com/org/repo/-/merge_requests/18","branch_name":"osmia/29885","tests_added":6},"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventResult,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				re, ok := ev.Parsed.(*ResultEvent)
				require.True(t, ok)
				assert.True(t, re.Success)
				assert.Equal(t, "Upgraded httpx", re.Summary)
				assert.Equal(t, "https://gitlab.com/org/repo/-/merge_requests/18", re.MergeRequestURL)
				assert.Equal(t, "osmia/29885", re.BranchName)
				assert.Equal(t, 6, re.TestsAdded)
				assert.Nil(t, re.StructuredOutput, "StructuredOutput should be cleared after merge")
			},
		},
		{
			name:     "result event with empty result but structured_output takes precedence",
			input:    `{"type":"result","is_error":false,"result":"","structured_output":{"success":true,"summary":"Done"},"timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventResult,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				re, ok := ev.Parsed.(*ResultEvent)
				require.True(t, ok)
				assert.True(t, re.Success)
				assert.Equal(t, "Done", re.Summary)
			},
		},
		{
			name:     "extra fields are ignored",
			input:    `{"type":"cost","input_tokens":100,"output_tokens":50,"cost_usd":0.001,"extra_field":"ignored","timestamp":"2026-01-01T00:00:00Z"}`,
			wantType: EventCost,
			checkFunc: func(t *testing.T, ev *StreamEvent) {
				ce, ok := ev.Parsed.(*CostEvent)
				require.True(t, ok)
				assert.Equal(t, 100, ce.InputTokens)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := ParseEvent([]byte(tt.input))
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, ev)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, ev)
			assert.Equal(t, tt.wantType, ev.Type)

			if tt.checkFunc != nil {
				tt.checkFunc(t, ev)
			}
		})
	}
}
