package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// maxErrorBodyBytes caps how much of an error response is quoted.
const maxErrorBodyBytes = 4 << 10

// Client is a client for an OpenAI-compatible chat completions API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient builds a client for the given base URL (for example
// http://localhost:1234/v1). The HTTP client must not set Timeout: a deadline
// on the whole request would cut off a long generation. Time limits belong to
// the context and to the transport.
func NewClient(httpClient *http.Client, baseURL string) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("http client must not be nil")
	}
	if httpClient.Timeout != 0 {
		return nil, errors.New("http client must not set Timeout: it would abort streaming responses")
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", baseURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("base URL %q: want absolute http or https URL", baseURL)
	}
	return &Client{httpClient: httpClient, baseURL: strings.TrimSuffix(baseURL, "/")}, nil
}

// StreamHandler receives streamed output. Every field may be nil.
type StreamHandler struct {
	// OnContent receives answer deltas as they arrive.
	OnContent func(delta string)
	// OnReasoning receives reasoning deltas as they arrive.
	OnReasoning func(delta string)
	// OnToolCall is called once per assembled tool call, after the stream
	// ends.
	OnToolCall func(call ToolCall)
}

// Completion is the assembled result of a streamed request.
type Completion struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// streamRequest adds the streaming options to a request.
type streamRequest struct {
	Request
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Stream sends the request with streaming enabled and assembles the response.
// Deltas are reported through h while they arrive.
func (c *Client) Stream(ctx context.Context, req Request, h StreamHandler) (Completion, error) {
	if strings.TrimSpace(req.Model) == "" {
		return Completion{}, errors.New("model must not be empty")
	}
	if len(req.Messages) == 0 {
		return Completion{}, errors.New("messages must not be empty")
	}

	payload, err := json.Marshal(streamRequest{
		Request:       req,
		Stream:        true,
		StreamOptions: streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return Completion{}, fmt.Errorf("encode request: %w", err)
	}

	endpoint := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, fmt.Errorf("build request %s: %w", endpoint, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Completion{}, fmt.Errorf("post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return Completion{}, fmt.Errorf("post %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	completion, err := assemble(resp.Body, h)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return completion, ctxErr
		}
		return completion, fmt.Errorf("stream %s: %w", endpoint, err)
	}
	return completion, nil
}

// assemble folds a stream into one completion.
func assemble(body io.Reader, h StreamHandler) (Completion, error) {
	var (
		content      strings.Builder
		reasoning    strings.Builder
		finishReason string
		usage        Usage
		pending      = map[int]*ToolCall{}
		order        []int
	)

	err := DecodeStream(body, func(chunk Chunk) error {
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if h.OnContent != nil {
					h.OnContent(choice.Delta.Content)
				}
			}
			if choice.Delta.ReasoningContent != "" {
				reasoning.WriteString(choice.Delta.ReasoningContent)
				if h.OnReasoning != nil {
					h.OnReasoning(choice.Delta.ReasoningContent)
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call, ok := pending[delta.Index]
				if !ok {
					call = &ToolCall{Type: FunctionType}
					pending[delta.Index] = call
					order = append(order, delta.Index)
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Type != "" {
					call.Type = delta.Type
				}
				if delta.Function.Name != "" {
					call.Function.Name += delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					call.Function.Arguments += delta.Function.Arguments
				}
			}
		}
		return nil
	})

	slices.Sort(order)
	calls := make([]ToolCall, 0, len(order))
	for _, index := range order {
		call := *pending[index]
		if call.Function.Name == "" {
			continue
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", index)
		}
		calls = append(calls, call)
	}

	completion := Completion{
		Message: Message{
			Role:             RoleAssistant,
			Content:          content.String(),
			ReasoningContent: reasoning.String(),
		},
		FinishReason: finishReason,
		Usage:        usage,
	}
	if len(calls) > 0 {
		completion.Message.ToolCalls = calls
	}
	if err != nil {
		return completion, err
	}
	if h.OnToolCall != nil {
		for _, call := range calls {
			h.OnToolCall(call)
		}
	}
	return completion, nil
}
