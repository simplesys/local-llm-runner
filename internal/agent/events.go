// Package agent implements the agent loop: it keeps the conversation, sends
// it to the model, executes the tool calls the model asks for and feeds the
// results back until the task is done or a limit is reached.
//
// The agent knows nothing about the terminal or about storage: it publishes
// events, and the interface, the session log and the metrics consume them.
package agent

import "github.com/simplesys/locallm/internal/llm"

// EventKind enumerates the agent notifications.
type EventKind string

// Event kinds.
const (
	// EventContent carries an answer delta in Text.
	EventContent EventKind = "content"
	// EventReasoning carries a reasoning delta in Text.
	EventReasoning EventKind = "reasoning"
	// EventToolStart announces a tool call in Tool.
	EventToolStart EventKind = "tool_start"
	// EventToolResult reports the outcome of a tool call in Text or Error.
	EventToolResult EventKind = "tool_result"
	// EventTurnDone reports the usage of one model request.
	EventTurnDone EventKind = "turn_done"
	// EventNotice carries a message for the user in Text.
	EventNotice EventKind = "notice"
)

// Event is one agent notification. Events are emitted from the goroutine that
// called Run.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      ToolRequest
	Usage     llm.Usage
	Turn      int
	Truncated bool
	Error     error
}

// ToolRequest describes a tool call the model wants to make.
type ToolRequest struct {
	ID        string
	Name      string
	Arguments string
	Mutating  bool
}

// StopReason says why Run returned.
type StopReason string

// Stop reasons.
const (
	StopDone      StopReason = "done"
	StopMaxTurns  StopReason = "max_turns"
	StopMaxTokens StopReason = "max_tokens"
	StopMaxTools  StopReason = "max_tools"
	StopDeadline  StopReason = "deadline"
	StopDenied    StopReason = "denied"
	StopCanceled  StopReason = "canceled"
)

// LimitReached reports whether the reason means the agent ran out of budget
// rather than finishing the task.
func (r StopReason) LimitReached() bool {
	switch r {
	case StopMaxTurns, StopMaxTokens, StopMaxTools, StopDeadline:
		return true
	case StopDone, StopDenied, StopCanceled:
		return false
	default:
		return false
	}
}
