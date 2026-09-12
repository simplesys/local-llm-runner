// Package llm is the client for OpenAI-compatible chat completion APIs
// (LM Studio by default): streaming requests, tool calls and token usage.
//
// LM Studio extends the OpenAI format, so unknown JSON fields are ignored
// rather than rejected, and the reasoning_content extension is kept apart
// from the answer.
//
// The package knows nothing about the agent, the tools or the terminal.
package llm

import "encoding/json"

// Role of a chat message.
type Role string

// Roles used in a conversation.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// FunctionType is the only tool type the API defines.
const FunctionType = "function"

// Message is one chat message. ReasoningContent is an LM Studio extension
// that is never sent back to the server, so it carries no JSON tag.
type Message struct {
	Role             Role       `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"-"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the function name and its raw JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string         `json:"type"`
	Function FunctionSchema `json:"function"`
}

// FunctionSchema is the name, description and JSON Schema of a tool.
type FunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage is the token accounting reported by the server.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add sums two usage records.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + other.PromptTokens,
		CompletionTokens: u.CompletionTokens + other.CompletionTokens,
		TotalTokens:      u.TotalTokens + other.TotalTokens,
	}
}

// Request is a chat completion request.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// Chunk is one streamed chat completion chunk.
type Chunk struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is one choice inside a chunk.
type Choice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

// Delta is the incremental part of a streamed message.
type Delta struct {
	Role             Role            `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolCallDelta is a partial tool call: its fields arrive across chunks and
// are joined by Index.
type ToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function FunctionCallDelta `json:"function"`
}

// FunctionCallDelta is the incremental part of a tool call.
type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
