// Package tools provides the tools the model can call: reading, searching
// and editing files, and running shell commands.
//
// All file access is confined to the workspace directory (see os.Root);
// commands run with a context, a timeout and bounded output capture.
//
// Status: planned (FR-8, FR-9, FR-11 in docs/project_description.md).
package tools
