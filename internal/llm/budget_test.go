package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBudgetCheck(t *testing.T) {
	tests := []struct {
		name      string
		maxUSD    float64
		spentUSD  float64
		expectErr bool
	}{
		{
			name:      "unlimited budget",
			maxUSD:    0,
			spentUSD:  100.0,
			expectErr: false,
		},
		{
			name:      "within budget",
			maxUSD:    1.0,
			spentUSD:  0.5,
			expectErr: false,
		},
		{
			name:      "at budget limit",
			maxUSD:    1.0,
			spentUSD:  1.0,
			expectErr: true,
		},
		{
			name:      "over budget",
			maxUSD:    1.0,
			spentUSD:  1.5,
			expectErr: true,
		},
		{
			name:      "fresh budget",
			maxUSD:    10.0,
			spentUSD:  0,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBudget(tt.maxUSD)
			b.SpentUSD = tt.spentUSD

			err := b.Check()
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "budget limit reached")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBudgetRecordTokens(t *testing.T) {
	b := NewBudget(1.0)

	b.RecordTokens(1000, 500)

	assert.Equal(t, 1000, b.TotalInputTokens)
	assert.Equal(t, 500, b.TotalOutputTokens)

	expectedCost := 1000*defaultInputCostPerToken + 500*defaultOutputCostPerToken
	assert.InDelta(t, expectedCost, b.Spent(), 0.0000001)

	// Record more tokens.
	b.RecordTokens(2000, 1000)
	assert.Equal(t, 3000, b.TotalInputTokens)
	assert.Equal(t, 1500, b.TotalOutputTokens)
}

func TestBudgetRemaining(t *testing.T) {
	b := NewBudget(1.0)
	assert.InDelta(t, 1.0, b.Remaining(), 0.0001)

	// 10k input at $3/M = $0.03, 5k output at $15/M = $0.075; total ~$0.105
	b.RecordTokens(10000, 5000)
	remaining := b.Remaining()
	spent := b.Spent()
	assert.Greater(t, spent, 0.0)
	assert.InDelta(t, 1.0-spent, remaining, 0.0001)
	assert.Less(t, remaining, 1.0)
	assert.Greater(t, remaining, 0.0)
}

func TestBudgetRemainingUnlimited(t *testing.T) {
	b := NewBudget(0)
	assert.Equal(t, -1.0, b.Remaining())
}

func TestBudgetReset(t *testing.T) {
	b := NewBudget(1.0)
	b.RecordTokens(1000, 500)

	require.Greater(t, b.Spent(), 0.0)

	b.Reset()

	assert.Equal(t, 0.0, b.Spent())
	assert.Equal(t, 0, b.TotalInputTokens)
	assert.Equal(t, 0, b.TotalOutputTokens)
}

func TestNewBudget(t *testing.T) {
	b := NewBudget(5.0)
	assert.Equal(t, 5.0, b.MaxUSD)
	assert.Equal(t, 0.0, b.SpentUSD)
	assert.Equal(t, defaultInputCostPerToken, b.InputCostPerToken)
	assert.Equal(t, defaultOutputCostPerToken, b.OutputCostPerToken)
}
