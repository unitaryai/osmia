package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CompletionRequest describes a single LLM completion call.
type CompletionRequest struct {
	// SystemPrompt is the system-level instruction.
	SystemPrompt string
	// UserMessage is the user-level message.
	UserMessage string
	// Model selects the model (e.g. "claude-sonnet-4-20250514").
	Model string
	// MaxTokens caps the response length.
	MaxTokens int
	// Temperature controls randomness (0.0–1.0).
	Temperature float64
}

// CompletionResponse holds the result of a completion call.
type CompletionResponse struct {
	// Content is the text response from the model.
	Content string
	// InputTokens is the number of input tokens consumed.
	InputTokens int
	// OutputTokens is the number of output tokens generated.
	OutputTokens int
	// Model is the model that actually served the request.
	Model string
}

// Client abstracts LLM API calls so subsystems can be tested with mock
// implementations.
type Client interface {
	// Complete sends a completion request and returns the response.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the response body from the Anthropic Messages API.
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// AnthropicClient implements Client using the Anthropic Messages API.
// It uses net/http directly — no external SDK dependency.
type AnthropicClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	// defaultModel is used when the request does not specify a model.
	defaultModel string
}

// AnthropicOption configures an AnthropicClient.
type AnthropicOption func(*AnthropicClient)

// WithBaseURL overrides the default API base URL. Useful for testing.
func WithBaseURL(url string) AnthropicOption {
	return func(c *AnthropicClient) { c.baseURL = url }
}

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(hc *http.Client) AnthropicOption {
	return func(c *AnthropicClient) { c.httpClient = hc }
}

// WithDefaultModel sets the default model when requests omit it.
func WithDefaultModel(model string) AnthropicOption {
	return func(c *AnthropicClient) { c.defaultModel = model }
}

// NewAnthropicClient creates an AnthropicClient with the given API key.
func NewAnthropicClient(apiKey string, opts ...AnthropicOption) *AnthropicClient {
	c := &AnthropicClient{
		apiKey:       apiKey,
		baseURL:      "https://api.anthropic.com",
		defaultModel: "claude-sonnet-4-20250514",
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Complete sends a completion request to the Anthropic Messages API.
func (c *AnthropicClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = c.defaultModel
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	body := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    req.SystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: req.UserMessage},
		},
		Temperature: req.Temperature,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", c.apiKey)
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshalling response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("api error (%s): %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	content := ""
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return &CompletionResponse{
		Content:      content,
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
		Model:        apiResp.Model,
	}, nil
}
