// Package agent implements the agent loop: it keeps the conversation,
// sends it to the model, executes the tool calls the model requests and
// feeds the results back until the task is done.
//
// The agent reports progress through events and never writes to the
// terminal itself; rendering is the job of package ui.
//
// Status: planned (FR-3, FR-7, FR-8, FR-9 in docs/project_description.md).
package agent
