// Package ui renders the agent session on the terminal: user input, streamed
// model output, tool activity, confirmations and the metrics summary.
//
// Only the model's answer goes to stdout; everything else goes to stderr, so
// that one-shot output can be redirected to a file or a pipe. A full-screen
// interface is added separately; this is the line-based one.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/simplesys/locallm/internal/agent"
)

// ANSI escapes used when color is enabled.
const (
	colorReset = "\x1b[0m"
	colorDim   = "\x1b[2m"
	colorBold  = "\x1b[1m"
	colorRed   = "\x1b[31m"
)

// maxArgumentsShown bounds how much of a tool call is echoed.
const maxArgumentsShown = 200

// Console renders the agent session on a plain line-based terminal.
type Console struct {
	// In is where user input is read from.
	In io.Reader
	// Out receives the model's answer and nothing else.
	Out io.Writer
	// Err receives prompts, progress and diagnostics.
	Err io.Writer
	// Color enables ANSI escapes.
	Color bool
	// Prompt is the input prompt; empty means "> ".
	Prompt string

	reader        *bufio.Reader
	openStream    bool
	openReasoning bool
}

// ShowPrompt prints the input prompt. Printing and reading are separate so
// that a session with a dedicated reader goroutine keeps all terminal output
// in one place.
func (c *Console) ShowPrompt() {
	prompt := c.Prompt
	if prompt == "" {
		prompt = "> "
	}
	c.endStream()
	c.endReasoning()
	fmt.Fprint(c.Err, c.style(colorBold, prompt))
}

// ReadLine reads one line of user input. It reports false at end of input.
func (c *Console) ReadLine() (string, bool, error) {
	line, err := c.readRawLine()
	if errors.Is(err, io.EOF) {
		// ReadLine never writes: an interactive session reads in its own
		// goroutine, and two writers on one stream would interleave.
		if strings.TrimSpace(line) == "" {
			return "", false, nil
		}
		return strings.TrimSpace(line), true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), true, nil
}

func (c *Console) readRawLine() (string, error) {
	if c.reader == nil {
		if c.In == nil {
			return "", io.EOF
		}
		c.reader = bufio.NewReader(c.In)
	}
	return c.reader.ReadString('\n')
}

// AskApproval prints the confirmation question for a mutating tool call.
func (c *Console) AskApproval(call agent.ToolRequest) {
	c.endStream()
	c.endReasoning()
	fmt.Fprintf(c.Err, "%s\n", c.style(colorDim, "  "+clip(compactSpace(call.Arguments), maxArgumentsShown)))
	fmt.Fprintf(c.Err, "allow %s? [y/N] ", call.Name)
}

// Approve asks the user to confirm a mutating tool call and reads the answer.
// It is used where nothing else reads the input; a session with a reader
// goroutine calls AskApproval and Approved instead.
func (c *Console) Approve(_ context.Context, call agent.ToolRequest) (bool, error) {
	c.AskApproval(call)

	line, err := c.readRawLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return Approved(line), nil
}

// Approved reports whether an answer means yes. Anything else, including end
// of input, means no: an unattended session must not change files silently.
func Approved(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// Handle turns one agent event into terminal output.
func (c *Console) Handle(event agent.Event) {
	switch event.Kind {
	case agent.EventContent:
		c.endReasoning()
		c.openStream = true
		fmt.Fprint(c.Out, event.Text)
	case agent.EventReasoning:
		c.endStream()
		if !c.openReasoning {
			c.openReasoning = true
			fmt.Fprint(c.Err, c.style(colorDim, "· "))
		}
		fmt.Fprint(c.Err, c.style(colorDim, event.Text))
	case agent.EventToolStart:
		c.endStream()
		c.endReasoning()
		fmt.Fprintln(c.Err, c.style(colorDim, fmt.Sprintf("→ %s %s",
			event.Tool.Name, clip(compactSpace(event.Tool.Arguments), maxArgumentsShown))))
	case agent.EventToolResult:
		c.endStream()
		c.endReasoning()
		if event.Error != nil {
			fmt.Fprintln(c.Err, c.style(colorRed, fmt.Sprintf("← %s error: %s", event.Tool.Name, event.Error)))
			return
		}
		note := ""
		if event.Truncated {
			note = ", truncated"
		}
		fmt.Fprintln(c.Err, c.style(colorDim, fmt.Sprintf("← %s ok (%d bytes%s)", event.Tool.Name, len(event.Text), note)))
	case agent.EventTurnDone:
		c.endStream()
		c.endReasoning()
		fmt.Fprintln(c.Err, c.style(colorDim, fmt.Sprintf("· turn %d: %d+%d tokens",
			event.Turn, event.Usage.PromptTokens, event.Usage.CompletionTokens)))
	case agent.EventNotice:
		c.endStream()
		c.endReasoning()
		fmt.Fprintln(c.Err, c.style(colorDim, "· "+event.Text))
	}
}

// Notice prints a diagnostic line.
func (c *Console) Notice(format string, args ...any) {
	c.endStream()
	c.endReasoning()
	fmt.Fprintln(c.Err, c.style(colorDim, "· "+fmt.Sprintf(format, args...)))
}

// Warn prints a warning the user should not miss.
func (c *Console) Warn(format string, args ...any) {
	c.endStream()
	c.endReasoning()
	fmt.Fprintln(c.Err, c.style(colorRed, "! "+fmt.Sprintf(format, args...)))
}

// EndAnswer closes the streamed answer so that the next output starts on its
// own line.
func (c *Console) EndAnswer() {
	c.endStream()
	c.endReasoning()
}

// endStream terminates a partially written answer line.
func (c *Console) endStream() {
	if !c.openStream {
		return
	}
	c.openStream = false
	fmt.Fprintln(c.Out)
}

// endReasoning terminates a reasoning stream before another stderr item.
func (c *Console) endReasoning() {
	if !c.openReasoning {
		return
	}
	c.openReasoning = false
	fmt.Fprintln(c.Err)
}

// style wraps text into an escape sequence when color is enabled.
func (c *Console) style(code, text string) string {
	if !c.Color {
		return text
	}
	return code + text + colorReset
}

// SlashCommand is a parsed slash command.
type SlashCommand struct {
	Name string
	Args string
}

// ParseSlash reports whether the line is a slash command and parses it.
func ParseSlash(line string) (SlashCommand, bool) {
	if !strings.HasPrefix(line, "/") || len(line) < 2 {
		return SlashCommand{}, false
	}
	body := line[1:]
	if strings.HasPrefix(body, " ") || strings.HasPrefix(body, "/") {
		return SlashCommand{}, false
	}
	name, args, _ := strings.Cut(body, " ")
	return SlashCommand{Name: strings.ToLower(name), Args: strings.TrimSpace(args)}, true
}

// clip shortens text for echoing.
func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// compactSpace collapses whitespace so that a multi-line argument list stays
// on one line.
func compactSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
