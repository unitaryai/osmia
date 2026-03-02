package llm

import (
	"context"
	"fmt"
)

// Module is a composable LLM operation, inspired by DSPy modules. Each
// module takes named inputs, executes one or more LLM calls, and returns
// named outputs conforming to its signature.
type Module interface {
	// Forward executes the module with the given inputs.
	Forward(ctx context.Context, inputs map[string]any) (map[string]any, error)

	// GetSignature returns the signature this module implements.
	GetSignature() Signature
}

// Predict is the simplest module: a single LLM call with signature-based
// prompt formatting and structured response parsing.
type Predict struct {
	sig    Signature
	client Client
	budget *Budget
}

// NewPredict creates a Predict module for the given signature. If budget is
// nil, no cost tracking is applied.
func NewPredict(sig Signature, client Client, budget *Budget) *Predict {
	return &Predict{
		sig:    sig,
		client: client,
		budget: budget,
	}
}

// Forward formats the prompt from inputs, calls the LLM, and parses the
// structured response.
func (p *Predict) Forward(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	if p.budget != nil {
		if err := p.budget.Check(); err != nil {
			return nil, fmt.Errorf("budget exhausted: %w", err)
		}
	}

	systemPrompt, userMessage, err := FormatPrompt(p.sig, inputs)
	if err != nil {
		return nil, fmt.Errorf("formatting prompt: %w", err)
	}

	resp, err := p.client.Complete(ctx, CompletionRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		MaxTokens:    1024,
		Temperature:  0.0,
	})
	if err != nil {
		return nil, fmt.Errorf("llm completion failed: %w", err)
	}

	if p.budget != nil {
		p.budget.RecordTokens(resp.InputTokens, resp.OutputTokens)
	}

	outputs, err := ParseResponse(p.sig, resp.Content)
	if err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return outputs, nil
}

// GetSignature returns the signature this module implements.
func (p *Predict) GetSignature() Signature {
	return p.sig
}

// ChainOfThought wraps a Predict module, prepending a "reasoning" output
// field to encourage step-by-step thinking before producing the final answer.
// The reasoning is included in the output map under the "reasoning" key.
type ChainOfThought struct {
	inner *Predict
}

// NewChainOfThought creates a ChainOfThought module that wraps the given
// signature with an additional reasoning step.
func NewChainOfThought(sig Signature, client Client, budget *Budget) *ChainOfThought {
	// Add a reasoning field as the first output.
	cotSig := Signature{
		Name:        sig.Name,
		Description: sig.Description + "\n\nLet's think step by step. First explain your reasoning, then provide the answer.",
		InputFields: sig.InputFields,
		OutputFields: append(
			[]Field{{
				Name:        "reasoning",
				Description: "Step-by-step reasoning leading to the answer",
				Type:        FieldTypeString,
				Required:    true,
			}},
			sig.OutputFields...,
		),
	}

	return &ChainOfThought{
		inner: NewPredict(cotSig, client, budget),
	}
}

// Forward executes the chain-of-thought module. The returned map includes
// both the "reasoning" field and all original output fields.
func (c *ChainOfThought) Forward(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	return c.inner.Forward(ctx, inputs)
}

// GetSignature returns the augmented signature (with reasoning field).
func (c *ChainOfThought) GetSignature() Signature {
	return c.inner.sig
}
