package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/simplesys/locallm/internal/llm"
	"github.com/simplesys/locallm/internal/tools"
)

// model is the part of the LLM client the agent needs.
type model interface {
	Stream(ctx context.Context, req llm.Request, handler llm.StreamHandler) (llm.Completion, error)
}

// toolset is the part of the tool registry the agent needs.
type toolset interface {
	Get(name string) (tools.Tool, bool)
	List() []tools.Tool
}

// Limits caps how much one Run may consume. A zero field means no limit.
type Limits struct {
	MaxTurns     int
	MaxTokens    int
	MaxToolCalls int
	Deadline     time.Duration
}

// Options configures an agent.
type Options struct {
	// Model is the model identifier sent to the server.
	Model string
	// SystemPrompt is the system message; required.
	SystemPrompt string
	// Tools are the tools offered to the model; nil means none.
	Tools toolset
	// Limits caps one Run.
	Limits Limits
	// Approve is asked before every mutating tool call; nil approves all.
	Approve func(ctx context.Context, call ToolRequest) (bool, error)
	// Events receives progress notifications; nil discards them.
	Events func(event Event)
}

// Agent runs one conversation. It is not safe for concurrent use.
type Agent struct {
	model   model
	options Options

	messages []llm.Message
	usage    llm.Usage
}

// Outcome is the result of one Run.
type Outcome struct {
	Reply      string
	Turns      int
	ToolCalls  int
	Usage      llm.Usage
	StopReason StopReason
}

// New builds an agent with the system message already in place.
func New(llmModel model, options Options) (*Agent, error) {
	if llmModel == nil {
		return nil, errors.New("model must not be nil")
	}
	if strings.TrimSpace(options.Model) == "" {
		return nil, errors.New("model name must not be empty")
	}
	if strings.TrimSpace(options.SystemPrompt) == "" {
		return nil, errors.New("system prompt must not be empty")
	}
	agent := &Agent{model: llmModel, options: options}
	agent.messages = []llm.Message{{Role: llm.RoleSystem, Content: options.SystemPrompt}}
	return agent, nil
}

// Model returns the model the agent talks to.
func (a *Agent) Model() string { return a.options.Model }

// SetModel switches the model for the next request, keeping the
// conversation.
func (a *Agent) SetModel(name string) {
	if strings.TrimSpace(name) != "" {
		a.options.Model = name
	}
}

// History returns the conversation so far, including the system message.
func (a *Agent) History() []llm.Message {
	return append([]llm.Message(nil), a.messages...)
}

// Restore replaces the conversation, keeping the current system message when
// the restored one has none.
func (a *Agent) Restore(messages []llm.Message) {
	restored := make([]llm.Message, 0, len(messages)+1)
	if len(messages) == 0 || messages[0].Role != llm.RoleSystem {
		restored = append(restored, llm.Message{Role: llm.RoleSystem, Content: a.options.SystemPrompt})
	}
	restored = append(restored, messages...)
	a.messages = restored
}

// Reset drops everything but the system message.
func (a *Agent) Reset() {
	a.messages = []llm.Message{{Role: llm.RoleSystem, Content: a.options.SystemPrompt}}
	a.usage = llm.Usage{}
}

// Usage returns the tokens spent in this conversation.
func (a *Agent) Usage() llm.Usage { return a.usage }

// Run sends the user input and works until the model answers without tool
// calls, a limit is reached or ctx is done.
func (a *Agent) Run(ctx context.Context, input string) (Outcome, error) {
	if strings.TrimSpace(input) == "" {
		return Outcome{}, errors.New("input must not be empty")
	}
	deadlineSet := a.options.Limits.Deadline > 0
	if deadlineSet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.options.Limits.Deadline)
		defer cancel()
	}

	a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: input})
	outcome := Outcome{StopReason: StopDone}
	requestTools := a.requestTools()

	for {
		outcome.Turns++
		completion, err := a.model.Stream(ctx, llm.Request{
			Model:    a.options.Model,
			Messages: a.messages,
			Tools:    requestTools,
		}, llm.StreamHandler{
			OnContent:   func(delta string) { a.emit(Event{Kind: EventContent, Text: delta, Turn: outcome.Turns}) },
			OnReasoning: func(delta string) { a.emit(Event{Kind: EventReasoning, Text: delta, Turn: outcome.Turns}) },
		})
		if err != nil {
			if reason, stopped := a.stopReason(ctx, deadlineSet); stopped {
				outcome.StopReason = reason
				return a.finish(outcome), nil
			}
			return a.finish(outcome), fmt.Errorf("model request: %w", err)
		}

		a.usage = a.usage.Add(completion.Usage)
		outcome.Usage = outcome.Usage.Add(completion.Usage)
		a.messages = append(a.messages, completion.Message)
		outcome.Reply = completion.Message.Content
		a.emit(Event{Kind: EventTurnDone, Usage: completion.Usage, Turn: outcome.Turns})

		if len(completion.Message.ToolCalls) == 0 {
			return a.finish(outcome), nil
		}

		stop, err := a.runToolCalls(ctx, completion.Message.ToolCalls, &outcome)
		if err != nil {
			return a.finish(outcome), err
		}
		if stop != "" {
			outcome.StopReason = stop
			return a.finish(outcome), nil
		}
		if reason, reached := a.limitReached(outcome); reached {
			outcome.StopReason = reason
			a.emit(Event{Kind: EventNotice, Text: "stopped: " + string(reason), Turn: outcome.Turns})
			return a.finish(outcome), nil
		}
		if reason, stopped := a.stopReason(ctx, deadlineSet); stopped {
			outcome.StopReason = reason
			return a.finish(outcome), nil
		}
	}
}

// runToolCalls executes the calls of one turn in order. It returns a stop
// reason when the loop must end.
func (a *Agent) runToolCalls(ctx context.Context, calls []llm.ToolCall, outcome *Outcome) (StopReason, error) {
	for _, call := range calls {
		request := ToolRequest{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}
		tool, found := a.lookup(call.Function.Name)
		if found {
			request.Mutating = tool.Mutating
		}
		a.emit(Event{Kind: EventToolStart, Tool: request, Turn: outcome.Turns})

		if !found {
			text := fmt.Sprintf("unknown tool %q, available: %s", call.Function.Name, strings.Join(a.toolNames(), ", "))
			a.appendToolResult(call, text)
			a.emit(Event{Kind: EventToolResult, Tool: request, Error: errors.New(text), Turn: outcome.Turns})
			continue
		}

		if tool.Mutating && a.options.Approve != nil {
			approved, err := a.options.Approve(ctx, request)
			if err != nil {
				return "", fmt.Errorf("approve %s: %w", call.Function.Name, err)
			}
			if !approved {
				a.appendToolResult(call, "the user did not approve this action")
				a.emit(Event{Kind: EventNotice, Text: "action declined: " + call.Function.Name, Turn: outcome.Turns})
				return StopDenied, nil
			}
		}

		result, err := tool.Run(ctx, json.RawMessage(call.Function.Arguments))
		outcome.ToolCalls++
		if err != nil {
			text := "error: " + err.Error()
			a.appendToolResult(call, text)
			a.emit(Event{Kind: EventToolResult, Tool: request, Error: err, Turn: outcome.Turns})
			continue
		}
		a.appendToolResult(call, result.Output)
		a.emit(Event{
			Kind: EventToolResult, Tool: request, Text: result.Output,
			Truncated: result.Truncated, Turn: outcome.Turns,
		})
	}
	return "", nil
}

// appendToolResult records the outcome of a tool call as a tool message, so
// that the model always sees an answer to every call it made.
func (a *Agent) appendToolResult(call llm.ToolCall, text string) {
	a.messages = append(a.messages, llm.Message{
		Role:       llm.RoleTool,
		Content:    text,
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	})
}

// lookup finds a tool by name.
func (a *Agent) lookup(name string) (tools.Tool, bool) {
	if a.options.Tools == nil {
		return tools.Tool{}, false
	}
	return a.options.Tools.Get(name)
}

// toolNames lists the registered tool names.
func (a *Agent) toolNames() []string {
	if a.options.Tools == nil {
		return nil
	}
	list := a.options.Tools.List()
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Name)
	}
	return names
}

// requestTools converts the registry into the API representation.
func (a *Agent) requestTools() []llm.Tool {
	if a.options.Tools == nil {
		return nil
	}
	list := a.options.Tools.List()
	converted := make([]llm.Tool, 0, len(list))
	for _, tool := range list {
		converted = append(converted, llm.Tool{
			Type: llm.FunctionType,
			Function: llm.FunctionSchema{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		})
	}
	return converted
}

// limitReached reports whether a budget is exhausted.
func (a *Agent) limitReached(outcome Outcome) (StopReason, bool) {
	limits := a.options.Limits
	switch {
	case limits.MaxTurns > 0 && outcome.Turns >= limits.MaxTurns:
		return StopMaxTurns, true
	case limits.MaxTokens > 0 && outcome.Usage.TotalTokens >= limits.MaxTokens:
		return StopMaxTokens, true
	case limits.MaxToolCalls > 0 && outcome.ToolCalls >= limits.MaxToolCalls:
		return StopMaxTools, true
	default:
		return "", false
	}
}

// stopReason maps a finished context to a stop reason.
func (a *Agent) stopReason(ctx context.Context, deadlineSet bool) (StopReason, bool) {
	switch {
	case deadlineSet && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return StopDeadline, true
	case ctx.Err() != nil:
		return StopCanceled, true
	default:
		return "", false
	}
}

// finish fills the accumulated counters into the outcome.
func (a *Agent) finish(outcome Outcome) Outcome {
	return outcome
}

// emit delivers an event when a consumer is interested.
func (a *Agent) emit(event Event) {
	if a.options.Events != nil {
		a.options.Events(event)
	}
}
