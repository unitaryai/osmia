# LLM Abstraction Layer

RoboDev includes a DSPy-inspired LLM abstraction package (`internal/llm/`) that provides typed, composable LLM interactions for all intelligent subsystems. Rather than making ad-hoc API calls, subsystems define **signatures** that declare their inputs and outputs, and the framework handles prompt formatting, response parsing, and cost tracking automatically.

## Why a Dedicated LLM Layer?

RoboDev's subsystems (PRM scoring, memory extraction, causal diagnosis, tournament judging) all need to call LLMs, but with very different schemas and cost constraints. Without a shared abstraction, each subsystem would re-implement prompt formatting, JSON parsing, error handling, and budget enforcement. The `internal/llm/` package provides this shared foundation.

**Key benefits:**

- **Type safety** — signatures define expected input/output fields with types, catching mismatches at build time rather than runtime
- **Composability** — modules can be chained (e.g. chain-of-thought wraps predict)
- **Budget enforcement** — per-subsystem spend limits prevent runaway costs
- **No external dependencies** — uses only `net/http` and `encoding/json` from the standard library
- **Testability** — the `Client` interface makes it trivial to mock LLM calls in tests

## Core Concepts

### Signatures

A **Signature** defines a typed contract for an LLM interaction, inspired by [DSPy](https://github.com/stanfordnlp/dspy):

```go
sig := llm.Signature{
    Name:        "ScoreToolCall",
    Description: "Evaluate an agent's recent tool call for productivity",
    InputFields: []llm.Field{
        {Name: "tool_calls", Description: "recent tool call sequence", Type: llm.FieldTypeString, Required: true},
        {Name: "context",    Description: "task context",              Type: llm.FieldTypeString, Required: false},
    },
    OutputFields: []llm.Field{
        {Name: "score",     Description: "productivity score 1-10",   Type: llm.FieldTypeInt,    Required: true},
        {Name: "reasoning", Description: "explanation of the score",  Type: llm.FieldTypeString, Required: true},
    },
}
```

The signature is validated before use — it must have a name and at least one output field. Input fields marked as `Required` are checked at prompt-formatting time.

### Field Types

| Type | Go Representation | Description |
|---|---|---|
| `string` | `string` | Free-form text |
| `int` | `int` | Integer values |
| `float` | `float64` | Floating-point values |
| `bool` | `bool` | Boolean values |
| `json` | `map[string]any` | Arbitrary JSON objects |

The adapter automatically coerces LLM responses to the declared type (e.g. JSON `7.0` → Go `int(7)` for `FieldTypeInt`).

### Modules

A **Module** is a composable LLM operation that takes named inputs and returns named outputs:

```go
type Module interface {
    Forward(ctx context.Context, inputs map[string]any) (map[string]any, error)
    GetSignature() Signature
}
```

Two built-in modules are provided:

#### Predict

The simplest module — a single LLM call with signature-based prompt formatting:

```go
predict := llm.NewPredict(sig, client, budget)
result, err := predict.Forward(ctx, map[string]any{
    "tool_calls": "Read main.go, Edit main.go, Bash go test",
    "context":    "fixing a nil pointer dereference",
})
// result["score"] = 8  (int)
// result["reasoning"] = "productive sequence: read, edit, test"  (string)
```

#### ChainOfThought

Wraps Predict with an additional "reasoning" step that encourages step-by-step thinking:

```go
cot := llm.NewChainOfThought(sig, client, budget)
result, err := cot.Forward(ctx, inputs)
// result["reasoning"] = "Step 1: the agent read the file..."
// result["score"] = 8
// result["verdict"] = "productive"
```

ChainOfThought automatically adds a `reasoning` output field and modifies the system prompt to encourage structured thinking before answering.

### Client

The `Client` interface abstracts LLM API calls:

```go
type Client interface {
    Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}
```

An `AnthropicClient` implementation is included, using `net/http` directly with no SDK dependency:

```go
client := llm.NewAnthropicClient("sk-ant-...",
    llm.WithDefaultModel("claude-sonnet-4-20250514"),
    llm.WithBaseURL("https://api.anthropic.com"),  // or custom endpoint
)
```

The client supports:

- API key authentication via the `X-API-Key` header
- Model selection per-request or via default
- Token counting in responses (input and output tokens)
- Context cancellation and timeouts
- Configurable HTTP client for proxies or custom TLS

### Budget

The `Budget` type tracks cumulative spend for a subsystem and refuses calls when the budget is exhausted:

```go
budget := llm.NewBudget(0.50) // $0.50 maximum

// Before each LLM call, the module checks:
err := budget.Check() // returns error if spent >= max

// After each call, tokens are recorded:
budget.RecordTokens(inputTokens, outputTokens)

// Query remaining budget:
remaining := budget.Remaining() // $0.35
spent := budget.Spent()         // $0.15
```

Budgets are thread-safe and can be shared across multiple modules within a subsystem.

## Prompt Formatting

The adapter converts a Signature + inputs into a structured LLM request:

**System prompt** (auto-generated):
```
You are executing the "ScoreToolCall" operation.
Evaluate an agent's recent tool call for productivity

## Output Format

Respond with a JSON object containing the following fields:

- `score` (int) (required): productivity score 1-10
- `reasoning` (string) (required): explanation of the score

Respond ONLY with the JSON object. No markdown fences, no explanation.
```

**User message** (auto-generated from inputs):
```
## tool_calls

(recent tool call sequence)

Read main.go, Edit main.go, Bash go test

## context

(task context)

fixing a nil pointer dereference
```

The response is expected to be a JSON object matching the output fields. The parser handles common LLM response quirks:

- Strips markdown code fences if present
- Coerces numeric types (JSON `float64` → Go `int`)
- Reports clear errors for missing required fields

## Architecture

```
internal/llm/
├── types.go       — Signature, Field, FieldType definitions
├── module.go      — Module interface, Predict, ChainOfThought
├── adapter.go     — Prompt formatting and response parsing
├── client.go      — Client interface + AnthropicClient
├── budget.go      — Budget tracking with spend limits
└── *_test.go      — Table-driven tests for all components
```

The entire package uses only standard library imports (`net/http`, `encoding/json`, `fmt`, `sync`, `context`). No external SDK dependencies.

## Usage by Subsystems

| Subsystem | How It Uses `internal/llm/` |
|---|---|
| **PRM (v2)** | `ChainOfThought` module with a scoring signature to evaluate agent tool calls |
| **Memory (v2)** | `Predict` module with an extraction signature to identify facts from TaskRun data |
| **Diagnosis (v2)** | `ChainOfThought` module with a classification signature to diagnose failure modes |
| **Tournament Judge** | `ChainOfThought` module with a judging signature to compare candidate diffs |

Each subsystem creates its own `Budget` to enforce per-subsystem cost limits independently.

## Testing

All components have table-driven unit tests:

- **types_test.go** — signature validation, field name helpers
- **adapter_test.go** — prompt formatting, response parsing, type coercion, edge cases (markdown fences, missing fields)
- **client_test.go** — HTTP request/response validation using `httptest.NewServer`, error handling, context cancellation
- **module_test.go** — Predict and ChainOfThought with mock clients, budget integration
- **budget_test.go** — spend tracking, limit enforcement, unlimited budgets, reset

To run the tests:

```bash
go test ./internal/llm/... -v
```
