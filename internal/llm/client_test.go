package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicClientComplete(t *testing.T) {
	// Set up a mock server that returns a valid Anthropic response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers.
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-key", r.Header.Get("X-API-Key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("Anthropic-Version"))

		// Verify request body.
		var req anthropicRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "claude-sonnet-4-20250514", req.Model)
		assert.Equal(t, "test system", req.System)
		assert.Len(t, req.Messages, 1)
		assert.Equal(t, "user", req.Messages[0].Role)
		assert.Equal(t, "test message", req.Messages[0].Content)

		resp := anthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: `{"score": 8}`},
			},
			Model: "claude-sonnet-4-20250514",
			Usage: struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			}{
				InputTokens:  100,
				OutputTokens: 50,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", WithBaseURL(server.URL))

	resp, err := client.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "test system",
		UserMessage:  "test message",
		MaxTokens:    512,
	})

	require.NoError(t, err)
	assert.Equal(t, `{"score": 8}`, resp.Content)
	assert.Equal(t, 100, resp.InputTokens)
	assert.Equal(t, 50, resp.OutputTokens)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Model)
}

func TestAnthropicClientAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"type": "invalid_request", "message": "bad input"}}`))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", WithBaseURL(server.URL))

	_, err := client.Complete(context.Background(), CompletionRequest{
		UserMessage: "test",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestAnthropicClientContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Simulate slow response — the context should cancel before this returns.
		select {}
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", WithBaseURL(server.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := client.Complete(ctx, CompletionRequest{
		UserMessage: "test",
	})

	assert.Error(t, err)
}

func TestAnthropicClientOptions(t *testing.T) {
	customHTTP := &http.Client{}
	client := NewAnthropicClient("key",
		WithBaseURL("https://custom.api.com"),
		WithHTTPClient(customHTTP),
		WithDefaultModel("custom-model"),
	)

	assert.Equal(t, "https://custom.api.com", client.baseURL)
	assert.Equal(t, customHTTP, client.httpClient)
	assert.Equal(t, "custom-model", client.defaultModel)
}

func TestAnthropicClientDefaultMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, 1024, req.MaxTokens)

		resp := anthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: "ok"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewAnthropicClient("key", WithBaseURL(server.URL))
	_, err := client.Complete(context.Background(), CompletionRequest{
		UserMessage: "test",
		// MaxTokens not set — should default to 1024.
	})
	require.NoError(t, err)
}
