package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/agent"
	"github.com/simplesys/locallm/internal/llm"
	"github.com/simplesys/locallm/internal/tools"
)

var update = flag.Bool("update", false, "update golden files")

// stubModel replays scripted completions and records the requests it saw.
type stubModel struct {
	replies  []llm.Completion
	err      error
	delay    time.Duration
	calls    int
	requests []llm.Request
}

func (m *stubModel) Stream(ctx context.Context, req llm.Request, handler llm.StreamHandler) (llm.Completion, error) {
	m.calls++
	m.requests = append(m.requests, req)
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return llm.Completion{}, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.Completion{}, err
	}
	if m.err != nil {
		return llm.Completion{}, m.err
	}
	index := min(m.calls-1, len(m.replies)-1)
	reply := m.replies[index]
	if handler.OnContent != nil && reply.Message.Content != "" {
		handler.OnContent(reply.Message.Content)
	}
	return reply, nil
}

// recordingTool counts its invocations.
type recordingTool struct {
	calls int
	args  []string
	err   error
}

func (r *recordingTool) tool(name string, mutating bool) tools.Tool {
	return tools.Tool{
		Name:     name,
		Schema:   json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Mutating: mutating,
		Run: func(_ context.Context, args json.RawMessage) (tools.Result, error) {
			r.calls++
			r.args = append(r.args, string(args))
			if r.err != nil {
				return tools.Result{}, r.err
			}
			return tools.Result{Output: name + " done"}, nil
		},
	}
}

func registry(t *testing.T, list ...tools.Tool) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.AddAll(list); err != nil {
		t.Fatalf("AddAll() error = %v", err)
	}
	return reg
}

func answer(text string, usage llm.Usage) llm.Completion {
	return llm.Completion{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: text},
		FinishReason: "stop",
		Usage:        usage,
	}
}

func toolRequest(name, args string, usage llm.Usage) llm.Completion {
	return llm.Completion{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:       "call_1",
				Type:     llm.FunctionType,
				Function: llm.FunctionCall{Name: name, Arguments: args},
			}},
		},
		FinishReason: "tool_calls",
		Usage:        usage,
	}
}

func newAgent(t *testing.T, llmModel *stubModel, options agent.Options) *agent.Agent {
	t.Helper()
	if options.Model == "" {
		options.Model = "qwen3"
	}
	if options.SystemPrompt == "" {
		options.SystemPrompt = "system"
	}
	created, err := agent.New(llmModel, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return created
}

func TestRunPlainAnswer(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("hello", llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})}}
	created := newAgent(t, llmModel, agent.Options{})

	outcome, err := created.Run(t.Context(), "hi")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.StopReason != agent.StopDone || outcome.Turns != 1 || outcome.Reply != "hello" {
		t.Errorf("Outcome = %+v, want one turn answering hello", outcome)
	}
	if outcome.Usage.TotalTokens != 12 {
		t.Errorf("Usage = %+v, want 12 total tokens", outcome.Usage)
	}
	history := created.History()
	if len(history) != 3 || history[0].Role != llm.RoleSystem || history[1].Role != llm.RoleUser || history[2].Role != llm.RoleAssistant {
		t.Errorf("History() = %+v, want system, user, assistant", history)
	}
}

func TestRunExecutesToolCall(t *testing.T) {
	t.Parallel()

	recorder := &recordingTool{}
	llmModel := &stubModel{replies: []llm.Completion{
		toolRequest("read_file", `{"path":"go.mod"}`, llm.Usage{TotalTokens: 30}),
		answer("done", llm.Usage{TotalTokens: 10}),
	}}
	created := newAgent(t, llmModel, agent.Options{Tools: registry(t, recorder.tool("read_file", false))})

	outcome, err := created.Run(t.Context(), "read the module file")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Turns != 2 || outcome.ToolCalls != 1 || outcome.StopReason != agent.StopDone {
		t.Errorf("Outcome = %+v, want two turns and one tool call", outcome)
	}
	if recorder.calls != 1 || recorder.args[0] != `{"path":"go.mod"}` {
		t.Errorf("tool was called %d times with %v, want once with the model arguments", recorder.calls, recorder.args)
	}

	history := created.History()
	last := history[len(history)-2]
	if last.Role != llm.RoleTool || last.ToolCallID != "call_1" || last.Content != "read_file done" {
		t.Errorf("tool message = %+v, want the result linked to call_1", last)
	}
	if got := llmModel.requests[0].Tools; len(got) != 1 || got[0].Function.Name != "read_file" {
		t.Errorf("request tools = %+v, want the registry converted", got)
	}
	if string(llmModel.requests[0].Tools[0].Function.Parameters) == "" {
		t.Error("request tool schema is empty, want the tool schema passed through")
	}
}

func TestRunExecutesSeveralToolCallsInOrder(t *testing.T) {
	t.Parallel()

	recorder := &recordingTool{}
	multi := llm.Completion{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "a", Type: llm.FunctionType, Function: llm.FunctionCall{Name: "first", Arguments: "{}"}},
				{ID: "b", Type: llm.FunctionType, Function: llm.FunctionCall{Name: "second", Arguments: "{}"}},
			},
		},
		FinishReason: "tool_calls",
	}
	llmModel := &stubModel{replies: []llm.Completion{multi, answer("done", llm.Usage{})}}
	created := newAgent(t, llmModel, agent.Options{
		Tools: registry(t, recorder.tool("first", false), recorder.tool("second", false)),
	})

	if _, err := created.Run(t.Context(), "do both"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	history := created.History()
	var toolMessages []llm.Message
	for _, message := range history {
		if message.Role == llm.RoleTool {
			toolMessages = append(toolMessages, message)
		}
	}
	if len(toolMessages) != 2 || toolMessages[0].Name != "first" || toolMessages[1].Name != "second" {
		t.Errorf("tool messages = %+v, want first then second", toolMessages)
	}
}

func TestRunUnknownTool(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{
		toolRequest("nope", "{}", llm.Usage{}),
		answer("recovered", llm.Usage{}),
	}}
	created := newAgent(t, llmModel, agent.Options{Tools: registry(t)})

	outcome, err := created.Run(t.Context(), "try")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.StopReason != agent.StopDone || outcome.Reply != "recovered" {
		t.Errorf("Outcome = %+v, want the loop to continue after an unknown tool", outcome)
	}
	history := created.History()
	if !strings.Contains(history[len(history)-2].Content, "unknown tool") {
		t.Errorf("tool message = %q, want it to report the unknown tool", history[len(history)-2].Content)
	}
}

func TestRunToolError(t *testing.T) {
	t.Parallel()

	recorder := &recordingTool{err: errors.New("file is missing")}
	llmModel := &stubModel{replies: []llm.Completion{
		toolRequest("read_file", "{}", llm.Usage{}),
		answer("handled", llm.Usage{}),
	}}
	created := newAgent(t, llmModel, agent.Options{Tools: registry(t, recorder.tool("read_file", false))})

	if _, err := created.Run(t.Context(), "read"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	history := created.History()
	if !strings.Contains(history[len(history)-2].Content, "error: file is missing") {
		t.Errorf("tool message = %q, want the tool error passed to the model", history[len(history)-2].Content)
	}
}

func TestRunApproval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		approve    func(context.Context, agent.ToolRequest) (bool, error)
		wantCalls  int
		wantReason agent.StopReason
	}{
		{name: "approved", approve: func(context.Context, agent.ToolRequest) (bool, error) { return true, nil }, wantCalls: 1, wantReason: agent.StopDone},
		{name: "declined", approve: func(context.Context, agent.ToolRequest) (bool, error) { return false, nil }, wantCalls: 0, wantReason: agent.StopDenied},
		{name: "no approver", approve: nil, wantCalls: 1, wantReason: agent.StopDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := &recordingTool{}
			llmModel := &stubModel{replies: []llm.Completion{
				toolRequest("write_file", "{}", llm.Usage{}),
				answer("done", llm.Usage{}),
			}}
			created := newAgent(t, llmModel, agent.Options{
				Tools:   registry(t, recorder.tool("write_file", true)),
				Approve: tt.approve,
			})
			outcome, err := created.Run(t.Context(), "write it")
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if outcome.StopReason != tt.wantReason {
				t.Errorf("StopReason = %q, want %q", outcome.StopReason, tt.wantReason)
			}
			if recorder.calls != tt.wantCalls {
				t.Errorf("tool calls = %d, want %d", recorder.calls, tt.wantCalls)
			}
		})
	}
}

func TestRunLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits agent.Limits
		want   agent.StopReason
	}{
		{name: "turns", limits: agent.Limits{MaxTurns: 2}, want: agent.StopMaxTurns},
		{name: "tokens", limits: agent.Limits{MaxTokens: 50}, want: agent.StopMaxTokens},
		{name: "tool calls", limits: agent.Limits{MaxToolCalls: 2}, want: agent.StopMaxTools},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := &recordingTool{}
			llmModel := &stubModel{replies: []llm.Completion{
				toolRequest("read_file", "{}", llm.Usage{TotalTokens: 30}),
			}}
			created := newAgent(t, llmModel, agent.Options{
				Tools:  registry(t, recorder.tool("read_file", false)),
				Limits: tt.limits,
			})
			outcome, err := created.Run(t.Context(), "loop forever")
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if outcome.StopReason != tt.want {
				t.Errorf("StopReason = %q, want %q", outcome.StopReason, tt.want)
			}
			if !outcome.StopReason.LimitReached() {
				t.Errorf("LimitReached() = false for %q, want true", outcome.StopReason)
			}
		})
	}
}

func TestRunLimitsApplyToSingleRun(t *testing.T) {
	t.Parallel()

	recorder := &recordingTool{}
	llmModel := &stubModel{replies: []llm.Completion{
		toolRequest("read_file", `{"path":"first"}`, llm.Usage{TotalTokens: 30}),
		answer("first done", llm.Usage{TotalTokens: 10}),
		toolRequest("read_file", `{"path":"second"}`, llm.Usage{TotalTokens: 30}),
		answer("second done", llm.Usage{TotalTokens: 10}),
	}}
	created := newAgent(t, llmModel, agent.Options{
		Tools: registry(t, recorder.tool("read_file", false)),
		Limits: agent.Limits{
			MaxTokens:    50,
			MaxToolCalls: 2,
		},
	})

	first, err := created.Run(t.Context(), "first")
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	second, err := created.Run(t.Context(), "second")
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if first.StopReason != agent.StopDone || second.StopReason != agent.StopDone {
		t.Fatalf("StopReason = %q, %q, want both runs done", first.StopReason, second.StopReason)
	}
	if first.Usage.TotalTokens != 40 || second.Usage.TotalTokens != 40 {
		t.Errorf("Usage = %+v, %+v, want each run to report only its own tokens", first.Usage, second.Usage)
	}
	if total := created.Usage().TotalTokens; total != 80 {
		t.Errorf("Usage() = %d total tokens, want the conversation total 80", total)
	}
	if recorder.calls != 2 {
		t.Errorf("tool calls = %d, want one call per run", recorder.calls)
	}
}

func TestRunDeadline(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("late", llm.Usage{})}, delay: time.Second}
	created := newAgent(t, llmModel, agent.Options{Limits: agent.Limits{Deadline: 20 * time.Millisecond}})

	outcome, err := created.Run(t.Context(), "slow")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.StopReason != agent.StopDeadline {
		t.Errorf("StopReason = %q, want %q", outcome.StopReason, agent.StopDeadline)
	}
}

func TestRunCanceled(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("late", llm.Usage{})}, delay: time.Second}
	created := newAgent(t, llmModel, agent.Options{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	outcome, err := created.Run(ctx, "stop")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.StopReason != agent.StopCanceled {
		t.Errorf("StopReason = %q, want %q", outcome.StopReason, agent.StopCanceled)
	}
}

func TestRunModelError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("connection refused")
	llmModel := &stubModel{err: wantErr}
	created := newAgent(t, llmModel, agent.Options{})

	if _, err := created.Run(t.Context(), "hi"); !errors.Is(err, wantErr) {
		t.Errorf("Run() error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestRunEvents(t *testing.T) {
	t.Parallel()

	recorder := &recordingTool{}
	llmModel := &stubModel{replies: []llm.Completion{
		toolRequest("read_file", "{}", llm.Usage{TotalTokens: 30}),
		answer("done", llm.Usage{TotalTokens: 10}),
	}}
	var kinds []agent.EventKind
	created := newAgent(t, llmModel, agent.Options{
		Tools:  registry(t, recorder.tool("read_file", false)),
		Events: func(event agent.Event) { kinds = append(kinds, event.Kind) },
	})
	if _, err := created.Run(t.Context(), "read"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []agent.EventKind{
		agent.EventTurnDone, agent.EventToolStart, agent.EventToolResult,
		agent.EventContent, agent.EventTurnDone,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Errorf("events = %v, want %v", kinds, want)
			break
		}
	}
}

func TestRestoreAndReset(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("ok", llm.Usage{})}}
	created := newAgent(t, llmModel, agent.Options{})
	created.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: "earlier question"},
		{Role: llm.RoleAssistant, Content: "earlier answer"},
	})
	if history := created.History(); len(history) != 3 || history[0].Role != llm.RoleSystem {
		t.Fatalf("History() = %+v, want the system message prepended once", created.History())
	}
	if _, err := created.Run(t.Context(), "next"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := len(created.History()); got != 5 {
		t.Errorf("len(History()) = %d, want 5", got)
	}

	created.Reset()
	if history := created.History(); len(history) != 1 || history[0].Role != llm.RoleSystem {
		t.Errorf("History() after Reset() = %+v, want only the system message", history)
	}
}

func TestRestoreKeepsRestoredSystemMessage(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("ok", llm.Usage{})}}
	created := newAgent(t, llmModel, agent.Options{})
	created.Restore([]llm.Message{{Role: llm.RoleSystem, Content: "restored"}, {Role: llm.RoleUser, Content: "q"}})
	history := created.History()
	if len(history) != 2 || history[0].Content != "restored" {
		t.Errorf("History() = %+v, want the restored system message kept", history)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()

	if _, err := agent.New(nil, agent.Options{Model: "m", SystemPrompt: "s"}); err == nil {
		t.Error("New(nil) error = nil, want an error")
	}
	if _, err := agent.New(&stubModel{}, agent.Options{SystemPrompt: "s"}); err == nil {
		t.Error("New() error = nil, want an error without a model name")
	}
	if _, err := agent.New(&stubModel{}, agent.Options{Model: "m"}); err == nil {
		t.Error("New() error = nil, want an error without a system prompt")
	}
	created := newAgent(t, &stubModel{replies: []llm.Completion{answer("x", llm.Usage{})}}, agent.Options{})
	if _, err := created.Run(t.Context(), "  "); err == nil {
		t.Error("Run(\"  \") error = nil, want an error")
	}
}

func TestSetModel(t *testing.T) {
	t.Parallel()

	llmModel := &stubModel{replies: []llm.Completion{answer("ok", llm.Usage{})}}
	created := newAgent(t, llmModel, agent.Options{})
	created.SetModel("other-model")
	created.SetModel("")
	if created.Model() != "other-model" {
		t.Errorf("Model() = %q, want %q", created.Model(), "other-model")
	}
	if _, err := created.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if llmModel.requests[0].Model != "other-model" {
		t.Errorf("request model = %q, want %q", llmModel.requests[0].Model, "other-model")
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	prompt := agent.BuildSystemPrompt(agent.PromptData{
		Workspace:    "/Users/dev/project",
		OS:           "darwin",
		Shell:        "sh -c",
		SandboxLevel: "seatbelt",
		Instructions: "Use tabs.",
	})

	const golden = "testdata/system_prompt.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(prompt), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if prompt != string(want) {
		t.Errorf("BuildSystemPrompt() =\n%s\nwant\n%s", prompt, want)
	}

	without := agent.BuildSystemPrompt(agent.PromptData{Workspace: "/x", OS: "linux", Shell: "sh -c", SandboxLevel: "none"})
	if strings.Contains(without, "Project instructions") {
		t.Errorf("BuildSystemPrompt() = %q, want no instructions section when there are none", without)
	}
}
