package llm

import (
	"fmt"
	"sync"
)

// Default per-token costs in USD. These are rough estimates and should be
// overridden via configuration for production use.
const (
	defaultInputCostPerToken  = 0.000003  // $3 per million input tokens
	defaultOutputCostPerToken = 0.000015  // $15 per million output tokens
)

// Budget tracks cumulative spend for a subsystem and refuses calls when the
// budget is exhausted. It is safe for concurrent use.
type Budget struct {
	mu sync.Mutex

	// MaxUSD is the maximum allowed spend. Zero means unlimited.
	MaxUSD float64
	// SpentUSD is the cumulative spend so far.
	SpentUSD float64
	// InputCostPerToken overrides the default input token cost.
	InputCostPerToken float64
	// OutputCostPerToken overrides the default output token cost.
	OutputCostPerToken float64
	// TotalInputTokens tracks cumulative input tokens.
	TotalInputTokens int
	// TotalOutputTokens tracks cumulative output tokens.
	TotalOutputTokens int
}

// NewBudget creates a budget with the given maximum spend in USD.
// Pass 0 for unlimited budget.
func NewBudget(maxUSD float64) *Budget {
	return &Budget{
		MaxUSD:             maxUSD,
		InputCostPerToken:  defaultInputCostPerToken,
		OutputCostPerToken: defaultOutputCostPerToken,
	}
}

// Check returns an error if the budget has been exhausted. Call this
// before making an LLM request.
func (b *Budget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.MaxUSD <= 0 {
		return nil // unlimited
	}

	if b.SpentUSD >= b.MaxUSD {
		return fmt.Errorf("budget limit reached: spent $%.4f of $%.4f", b.SpentUSD, b.MaxUSD)
	}

	return nil
}

// RecordTokens updates the cumulative spend based on token counts.
func (b *Budget) RecordTokens(inputTokens, outputTokens int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.TotalInputTokens += inputTokens
	b.TotalOutputTokens += outputTokens

	inputCost := float64(inputTokens) * b.InputCostPerToken
	outputCost := float64(outputTokens) * b.OutputCostPerToken
	b.SpentUSD += inputCost + outputCost
}

// Remaining returns the remaining budget in USD, or -1 if unlimited.
func (b *Budget) Remaining() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.MaxUSD <= 0 {
		return -1
	}

	remaining := b.MaxUSD - b.SpentUSD
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Spent returns the current cumulative spend in USD.
func (b *Budget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.SpentUSD
}

// Reset zeroes out all tracked spend and token counts.
func (b *Budget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.SpentUSD = 0
	b.TotalInputTokens = 0
	b.TotalOutputTokens = 0
}
