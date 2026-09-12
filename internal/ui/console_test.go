package ui_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/agent"
	"github.com/simplesys/locallm/internal/llm"
	"github.com/simplesys/locallm/internal/ui"
)

// newConsole builds a console over string buffers.
func newConsole(input string) (*ui.Console, *strings.Builder, *strings.Builder) {
	var out, errOut strings.Builder
	console := &ui.Console{In: strings.NewReader(input), Out: &out, Err: &errOut}
	return console, &out, &errOut
}

func TestHandleRoutesOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    agent.Event
		wantOut  string
		wantErr  string
		emptyOut bool
	}{
		{
			name:    "answer goes to stdout",
			event:   agent.Event{Kind: agent.EventContent, Text: "hello"},
			wantOut: "hello",
		},
		{
			name:     "reasoning goes to stderr",
			event:    agent.Event{Kind: agent.EventReasoning, Text: "thinking"},
			wantErr:  "· thinking",
			emptyOut: true,
		},
		{
			name: "tool start goes to stderr",
			event: agent.Event{
				Kind: agent.EventToolStart,
				Tool: agent.ToolRequest{Name: "read_file", Arguments: "{\n  \"path\": \"go.mod\"\n}"},
			},
			wantErr:  `→ read_file { "path": "go.mod" }`,
			emptyOut: true,
		},
		{
			name: "tool result reports size",
			event: agent.Event{
				Kind: agent.EventToolResult,
				Tool: agent.ToolRequest{Name: "read_file"},
				Text: "12345",
			},
			wantErr:  "← read_file ok (5 bytes)",
			emptyOut: true,
		},
		{
			name: "tool error is visible",
			event: agent.Event{
				Kind:  agent.EventToolResult,
				Tool:  agent.ToolRequest{Name: "write_file"},
				Error: errors.New("outside the workspace"),
			},
			wantErr:  "← write_file error: outside the workspace",
			emptyOut: true,
		},
		{
			name: "turn summary",
			event: agent.Event{
				Kind:  agent.EventTurnDone,
				Turn:  2,
				Usage: llm.Usage{PromptTokens: 5120, CompletionTokens: 870},
			},
			wantErr:  "· turn 2: 5120+870 tokens",
			emptyOut: true,
		},
		{
			name:     "notice",
			event:    agent.Event{Kind: agent.EventNotice, Text: "stopped: max_turns"},
			wantErr:  "· stopped: max_turns",
			emptyOut: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			console, out, errOut := newConsole("")
			console.Handle(tt.event)

			if tt.wantOut != "" && !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			if tt.emptyOut && out.String() != "" {
				t.Errorf("stdout = %q, want it empty", out.String())
			}
			if tt.wantErr != "" && !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}

func TestHandleClosesStreamedAnswer(t *testing.T) {
	t.Parallel()

	console, out, _ := newConsole("")
	console.Handle(agent.Event{Kind: agent.EventContent, Text: "partial"})
	console.Handle(agent.Event{Kind: agent.EventNotice, Text: "note"})
	if out.String() != "partial\n" {
		t.Errorf("stdout = %q, want the answer line closed", out.String())
	}

	console.Handle(agent.Event{Kind: agent.EventContent, Text: "more"})
	console.EndAnswer()
	console.EndAnswer()
	if out.String() != "partial\nmore\n" {
		t.Errorf("stdout = %q, want exactly one closing newline", out.String())
	}
}

func TestHandlePrefixesReasoningStreamOnce(t *testing.T) {
	t.Parallel()

	console, _, errOut := newConsole("")
	console.Handle(agent.Event{Kind: agent.EventReasoning, Text: "thinking"})
	console.Handle(agent.Event{Kind: agent.EventReasoning, Text: " more"})
	console.Handle(agent.Event{Kind: agent.EventNotice, Text: "done"})

	if got, want := errOut.String(), "· thinking more\n· done\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestNoColorByDefault(t *testing.T) {
	t.Parallel()

	console, out, errOut := newConsole("")
	console.Handle(agent.Event{Kind: agent.EventContent, Text: "answer"})
	console.Notice("note")
	console.Warn("warning")
	if strings.Contains(out.String()+errOut.String(), "\x1b[") {
		t.Errorf("output = %q%q, want no escape sequences when Color is off", out.String(), errOut.String())
	}
}

func TestColorEnabled(t *testing.T) {
	t.Parallel()

	var out, errOut strings.Builder
	console := &ui.Console{In: strings.NewReader(""), Out: &out, Err: &errOut, Color: true}
	console.Notice("note")
	if !strings.Contains(errOut.String(), "\x1b[") {
		t.Errorf("stderr = %q, want escape sequences when Color is on", errOut.String())
	}
}

func TestApprove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "y", input: "y\n", want: true},
		{name: "yes", input: "yes\n", want: true},
		{name: "uppercase", input: "Y\n", want: true},
		{name: "empty line", input: "\n"},
		{name: "no", input: "n\n"},
		{name: "end of input", input: ""},
		{name: "anything else", input: "maybe\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			console, _, errOut := newConsole(tt.input)
			got, err := console.Approve(t.Context(), agent.ToolRequest{Name: "write_file", Arguments: `{"path":"a"}`})
			if err != nil {
				t.Fatalf("Approve() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Approve() = %v, want %v", got, tt.want)
			}
			if !strings.Contains(errOut.String(), "allow write_file?") {
				t.Errorf("stderr = %q, want the confirmation prompt", errOut.String())
			}
		})
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()

	console, _, _ := newConsole("first\nsecond")
	text, ok, err := console.ReadLine()
	if err != nil || !ok || text != "first" {
		t.Fatalf("ReadLine() = %q, %v, %v, want \"first\", true, nil", text, ok, err)
	}
	text, ok, err = console.ReadLine()
	if err != nil || !ok || text != "second" {
		t.Fatalf("ReadLine() = %q, %v, %v, want \"second\", true, nil", text, ok, err)
	}
	text, ok, err = console.ReadLine()
	if err != nil || ok || text != "" {
		t.Fatalf("ReadLine() = %q, %v, %v, want \"\", false, nil at end of input", text, ok, err)
	}
}

func TestParseSlash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "bare command", line: "/help", wantName: "help", wantOK: true},
		{name: "command with argument", line: "/model qwen3-coder", wantName: "model", wantArgs: "qwen3-coder", wantOK: true},
		{name: "uppercase command", line: "/MODEL x", wantName: "model", wantArgs: "x", wantOK: true},
		{name: "space after slash", line: "/ model"},
		{name: "plain text", line: "fix the tests"},
		{name: "empty line", line: ""},
		{name: "lone slash", line: "/"},
		{name: "path", line: "//etc/hosts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command, ok := ui.ParseSlash(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ParseSlash(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if command.Name != tt.wantName || command.Args != tt.wantArgs {
				t.Errorf("ParseSlash(%q) = %+v, want name %q and args %q", tt.line, command, tt.wantName, tt.wantArgs)
			}
		})
	}
}
