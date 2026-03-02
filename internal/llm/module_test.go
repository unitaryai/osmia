package llm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockClient implements Client for testing.
type mockClient struct {
	response *CompletionResponse
	err      error
	calls    int
}

func (m *mockClient) Complete(_ context.Context, _ CompletionRequest) (*CompletionResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestPredictForward(t *testing.T) {
	sig := Signature{
		Name: "Score",
		InputFields: []Field{
			{Name: "input", Type: FieldTypeString, Required: true},
		},
		OutputFields: []Field{
			{Name: "score", Type: FieldTypeInt, Required: true},
			{Name: "reason", Type: FieldTypeString, Required: true},
		},
	}

	client := &mockClient{
		response: &CompletionResponse{
			Content:      `{"score": 7, "reason": "good work"}`,
			InputTokens:  50,
			OutputTokens: 20,
		},
	}

	predict := NewPredict(sig, client, nil)
	result, err := predict.Forward(context.Background(), map[string]any{
		"input": "test data",
	})

	require.NoError(t, err)
	assert.Equal(t, 7, result["score"])
	assert.Equal(t, "good work", result["reason"])
	assert.Equal(t, 1, client.calls)
}

func TestPredictWithBudget(t *testing.T) {
	sig := Signature{
		Name: "Score",
		InputFields: []Field{
			{Name: "input", Type: FieldTypeString, Required: true},
		},
		OutputFields: []Field{
			{Name: "score", Type: FieldTypeInt, Required: true},
		},
	}

	client := &mockClient{
		response: &CompletionResponse{
			Content:      `{"score": 5}`,
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	budget := NewBudget(0.01) // Very small budget.
	predict := NewPredict(sig, client, budget)

	result, err := predict.Forward(context.Background(), map[string]any{
		"input": "test",
	})
	require.NoError(t, err)
	assert.Equal(t, 5, result["score"])
	assert.Greater(t, budget.Spent(), 0.0)
}

func TestPredictBudgetExhausted(t *testing.T) {
	sig := Signature{
		Name:         "Score",
		InputFields:  []Field{{Name: "input", Type: FieldTypeString, Required: true}},
		OutputFields: []Field{{Name: "score", Type: FieldTypeInt, Required: true}},
	}

	client := &mockClient{
		response: &CompletionResponse{Content: `{"score": 5}`},
	}

	budget := NewBudget(0.0001) // Tiny budget.
	budget.SpentUSD = 0.0002    // Already over.

	predict := NewPredict(sig, client, budget)
	_, err := predict.Forward(context.Background(), map[string]any{
		"input": "test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "budget")
	assert.Equal(t, 0, client.calls) // Should not have called the API.
}

func TestPredictClientError(t *testing.T) {
	sig := Signature{
		Name:         "Score",
		InputFields:  []Field{{Name: "input", Type: FieldTypeString, Required: true}},
		OutputFields: []Field{{Name: "score", Type: FieldTypeInt, Required: true}},
	}

	client := &mockClient{
		err: fmt.Errorf("connection refused"),
	}

	predict := NewPredict(sig, client, nil)
	_, err := predict.Forward(context.Background(), map[string]any{
		"input": "test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestPredictGetSignature(t *testing.T) {
	sig := Signature{
		Name:         "TestSig",
		OutputFields: []Field{{Name: "out", Type: FieldTypeString}},
	}
	predict := NewPredict(sig, &mockClient{}, nil)
	assert.Equal(t, "TestSig", predict.GetSignature().Name)
}

func TestChainOfThoughtForward(t *testing.T) {
	sig := Signature{
		Name: "Evaluate",
		InputFields: []Field{
			{Name: "data", Type: FieldTypeString, Required: true},
		},
		OutputFields: []Field{
			{Name: "verdict", Type: FieldTypeString, Required: true},
		},
	}

	client := &mockClient{
		response: &CompletionResponse{
			Content:      `{"reasoning": "Step 1: analyse data. Step 2: conclude.", "verdict": "pass"}`,
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	cot := NewChainOfThought(sig, client, nil)
	result, err := cot.Forward(context.Background(), map[string]any{
		"data": "test data",
	})

	require.NoError(t, err)
	assert.Equal(t, "pass", result["verdict"])
	assert.Contains(t, result["reasoning"].(string), "Step 1")

	// Verify the signature was augmented with reasoning.
	cotSig := cot.GetSignature()
	assert.Equal(t, "reasoning", cotSig.OutputFields[0].Name)
	assert.Equal(t, "verdict", cotSig.OutputFields[1].Name)
}

func TestChainOfThoughtIncludesStepByStep(t *testing.T) {
	sig := Signature{
		Name:         "Test",
		Description:  "Original description",
		OutputFields: []Field{{Name: "result", Type: FieldTypeString}},
	}

	cot := NewChainOfThought(sig, &mockClient{
		response: &CompletionResponse{Content: `{"reasoning": "ok", "result": "done"}`},
	}, nil)

	cotSig := cot.GetSignature()
	assert.Contains(t, cotSig.Description, "step by step")
}
