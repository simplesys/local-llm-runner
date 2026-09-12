package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/simplesys/locallm/internal/tools"
)

func stubTool(name string) tools.Tool {
	return tools.Tool{
		Name: name,
		Run:  func(context.Context, json.RawMessage) (tools.Result, error) { return tools.Result{}, nil },
	}
}

func TestRegistry(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	if err := registry.AddAll([]tools.Tool{stubTool("read_file"), stubTool("grep")}); err != nil {
		t.Fatalf("AddAll() error = %v", err)
	}
	if err := registry.Add(stubTool("read_file")); err == nil {
		t.Error("Add() error = nil, want an error for a duplicate name")
	}
	if err := registry.Add(stubTool("")); err == nil {
		t.Error("Add() error = nil, want an error for an empty name")
	}
	if err := registry.Add(tools.Tool{Name: "broken"}); err == nil {
		t.Error("Add() error = nil, want an error for a tool without Run")
	}

	names := registry.Names()
	if len(names) != 2 || names[0] != "read_file" || names[1] != "grep" {
		t.Errorf("Names() = %v, want registration order", names)
	}
	if list := registry.List(); len(list) != 2 || list[0].Name != "read_file" {
		t.Errorf("List() = %v, want registration order", list)
	}
	if _, ok := registry.Get("grep"); !ok {
		t.Error("Get(grep) = false, want true")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Error("Get(missing) = true, want false")
	}
}

func TestToolSchemasAreValid(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})

	all := append(files.Tools(), files.SearchTools(tools.SearchOptions{})...)
	shell, err := tools.NewShell(tools.ShellOptions{Workspace: files.Workspace(), Sandbox: plainRunner{}})
	if err != nil {
		t.Fatalf("NewShell() error = %v", err)
	}
	all = append(all, shell)

	for _, tool := range all {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			var schema map[string]any
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				t.Fatalf("schema is not valid JSON: %v", err)
			}
			if schema["type"] != "object" {
				t.Errorf("schema type = %v, want object", schema["type"])
			}
			if _, ok := schema["properties"]; !ok {
				t.Error("schema has no properties")
			}
			if _, ok := schema["required"]; !ok {
				t.Error("schema has no required list")
			}
			if tool.Description == "" {
				t.Error("description is empty")
			}
		})
	}
}
