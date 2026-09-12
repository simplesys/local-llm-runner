// Package tools provides the tools the model can call: reading, searching and
// editing files, and running shell commands.
//
// All file access is confined to the workspace through os.Root, command
// output is bounded, and every tool reports truncation explicitly, because
// unbounded output would consume the context window the model needs.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Result is what a tool returns to the model.
type Result struct {
	// Output is the text handed to the model, already truncated.
	Output string
	// Truncated reports that Output is not the whole result.
	Truncated bool
}

// Tool is one callable tool.
type Tool struct {
	// Name is the identifier the model calls.
	Name string
	// Description tells the model when to use the tool.
	Description string
	// Schema is the JSON Schema of the arguments object.
	Schema json.RawMessage
	// Mutating marks tools that change files or run commands; those need
	// the user's approval.
	Mutating bool
	// Run executes the tool.
	Run func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Registry holds the tools available in a session. Tools are registered
// before the session starts, so the registry is not safe for concurrent
// modification, only for concurrent reads.
type Registry struct {
	order []string
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Add registers a tool. A duplicate or nameless tool is an error.
func (r *Registry) Add(tool Tool) error {
	if strings.TrimSpace(tool.Name) == "" {
		return errors.New("tool name must not be empty")
	}
	if tool.Run == nil {
		return fmt.Errorf("tool %s: Run must not be nil", tool.Name)
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %s is already registered", tool.Name)
	}
	r.tools[tool.Name] = tool
	r.order = append(r.order, tool.Name)
	return nil
}

// AddAll registers several tools, stopping at the first error.
func (r *Registry) AddAll(tools []Tool) error {
	for _, tool := range tools {
		if err := r.Add(tool); err != nil {
			return err
		}
	}
	return nil
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns the tools in registration order.
func (r *Registry) List() []Tool {
	tools := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		tools = append(tools, r.tools[name])
	}
	return tools
}

// Names returns the registered tool names in registration order.
func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

// decodeArgs parses tool arguments strictly: an unknown field usually means
// the model guessed the schema, and silently ignoring it hides the mistake.
func decodeArgs(raw json.RawMessage, target any) error {
	payload := bytes.TrimSpace(raw)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	return nil
}

// truncate shortens text to limit bytes and reports whether it had to.
func truncate(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	cut := text[:limit]
	if index := strings.LastIndexByte(cut, '\n'); index > limit/2 {
		cut = cut[:index]
	}
	omitted := len(text) - len(cut)
	return cut + fmt.Sprintf("\n... output truncated (%d bytes omitted)", omitted), true
}
