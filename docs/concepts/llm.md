# LLM abstraction layer

`internal/llm/` is a DSPy-inspired package for typed, composable LLM calls. The PRM, memory, diagnosis and tournament judge all use it. Each subsystem defines a signature that declares its inputs and outputs, and the package handles prompt formatting, response parsing and cost tracking.

## Why a dedicated LLM layer?

PRM scoring, memory extraction, causal diagnosis and tournament judging all call LLMs, with different schemas and cost constraints. Without a shared package, each would re-implement prompt formatting, JSON parsing, error handling and budget enforcement. `internal/llm/` provides those once.

What it gives you:

- Typed fields: signatures define expected input and output fields with types.
- Composable modules: chain-of-thought wraps predict, for example.
- Per-subsystem spend limits.
- No external dependencies: it uses only `net/http` and `encoding/json` from the standard library.
- A `Client` interface that is easy to mock in tests.

## Core concepts

### Signatures

A signature defines a typed contract for an LLM interaction, inspired by [DSPy](https://github.com/stanfordnlp/dspy):

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

The signature is validated before use: it must have a name and at least one output field. Input fields marked as `Required` are checked at prompt-formatting time.

### Field types

| Type | Go representation | Description |
|---|---|---|
| `string` | `string` | Free-form text |
| `int` | `int` | Integer values |
| `float` | `float64` | Floating-point values |
| `bool` | `bool` | Boolean values |
| `json` | `map[string]any` | Arbitrary JSON objects |

The adapter coerces LLM responses to the declared type (e.g. JSON `7.0` → Go `int(7)` for `FieldTypeInt`).

### Modules

A module is a composable LLM operation that takes named inputs and returns named outputs:

```go
type Module interface {
    Forward(ctx context.Context, inputs map[string]any) (map[string]any, error)
    GetSignature() Signature
}
```

Osmia provides two modules:

#### Predict

The simplest module: a single LLM call with signature-based prompt formatting:

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

Wraps Predict with an extra "reasoning" step that encourages step-by-step thinking:

```go
cot := llm.NewChainOfThought(sig, client, budget)
result, err := cot.Forward(ctx, inputs)
// result["reasoning"] = "Step 1: the agent read the file..."
// result["score"] = 8
// result["verdict"] = "productive"
```

ChainOfThought adds a `reasoning` output field and changes the system prompt to ask for structured thinking before the answer.

### Client

The `Client` interface abstracts LLM API calls:

```go
type Client interface {
    Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}
```

`AnthropicClient` is included. It uses `net/http` directly, with no SDK dependency:

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

The `Budget` type tracks cumulative spend for a subsystem and refuses calls once the budget is exhausted:

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

Budgets are thread-safe and can be shared by several modules in one subsystem.

## Prompt formatting

The adapter converts a signature and its inputs into a structured LLM request:

System prompt (generated):
```
You are executing the "ScoreToolCall" operation.
Evaluate an agent's recent tool call for productivity

## Output Format

Respond with a JSON object containing the following fields:

- `score` (int) (required): productivity score 1-10
- `reasoning` (string) (required): explanation of the score

Respond ONLY with the JSON object. No markdown fences, no explanation.
```

User message (generated from the inputs):
```
## tool_calls

(recent tool call sequence)

Read main.go, Edit main.go, Bash go test

## context

(task context)

fixing a nil pointer dereference
```

The response is expected to be a JSON object matching the output fields. The parser handles common quirks in LLM responses:

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

The package uses only standard library imports (`net/http`, `encoding/json`, `fmt`, `sync`, `context`).

## Usage by subsystems

| Subsystem | How it uses `internal/llm/` |
|---|---|
| PRM (v2) | `ChainOfThought` module with a scoring signature to evaluate agent tool calls |
| Memory (v2) | `Predict` module with an extraction signature to identify facts from TaskRun data |
| Diagnosis (v2) | `ChainOfThought` module with a classification signature to diagnose failure modes |
| Tournament judge | `ChainOfThought` module with a judging signature to compare candidate diffs |

Each subsystem creates its own `Budget`, so cost limits apply independently.

## Testing

Every component has table-driven unit tests:

- `types_test.go`: signature validation, field name helpers
- `adapter_test.go`: prompt formatting, response parsing, type coercion, edge cases (markdown fences, missing fields)
- `client_test.go`: HTTP request/response validation using `httptest.NewServer`, error handling, context cancellation
- `module_test.go`: Predict and ChainOfThought with mock clients, budget integration
- `budget_test.go`: spend tracking, limit enforcement, unlimited budgets, reset

To run the tests:

```bash
go test ./internal/llm/... -v
```
