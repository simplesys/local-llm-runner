package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/tools"
)

// newFiles opens a workspace with a small tree of test files. The workspace
// is closed through t.Cleanup, which runs after parallel subtests finish.
func newFiles(t *testing.T, opts tools.FilesOptions) *tools.Files {
	t.Helper()
	workspace := t.TempDir()
	write(t, workspace, "go.mod", "module example\n\ngo 1.27.0\n")
	write(t, workspace, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	write(t, workspace, "internal/app/app.go", "package app\n\nconst Name = \"app\"\n")
	write(t, workspace, "docs/readme.md", "# Docs\n\ntext\n")
	write(t, workspace, ".git/config", "[core]\n")
	write(t, workspace, "node_modules/lib/index.js", "module.exports = 1\n")

	files, err := tools.NewFiles(workspace, opts)
	if err != nil {
		t.Fatalf("NewFiles() error = %v", err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return files
}

func write(t *testing.T, workspace, name, content string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// call runs a tool by name with the given arguments.
func call(t *testing.T, list []tools.Tool, name, args string) (tools.Result, error) {
	t.Helper()
	for _, tool := range list {
		if tool.Name == name {
			return tool.Run(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("tool %s is not registered", name)
	return tools.Result{}, nil
}

func TestReadFile(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.Tools()

	result, err := call(t, list, "read_file", `{"path":"go.mod"}`)
	if err != nil {
		t.Fatalf("read_file error = %v", err)
	}
	if !strings.Contains(result.Output, "1\tmodule example") {
		t.Errorf("output = %q, want numbered lines", result.Output)
	}

	result, err = call(t, list, "read_file", `{"path":"main.go","offset":3,"limit":1}`)
	if err != nil {
		t.Fatalf("read_file error = %v", err)
	}
	if strings.Count(result.Output, "\n") != 1 || !strings.Contains(result.Output, "3\tfunc main()") {
		t.Errorf("output = %q, want only line 3", result.Output)
	}

	if _, err := call(t, list, "read_file", `{"path":"missing.go"}`); err == nil {
		t.Error("read_file error = nil, want an error for a missing file")
	}
	if _, err := call(t, list, "read_file", `{"path":"docs"}`); err == nil {
		t.Error("read_file error = nil, want an error for a directory")
	}
}

func TestReadFileLimits(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{MaxReadBytes: 64, MaxOutputBytes: 200})
	write(t, files.Workspace(), "big.txt", strings.Repeat("0123456789\n", 100))
	write(t, files.Workspace(), "binary.bin", "abc\x00def")
	write(t, files.Workspace(), "wide.txt", strings.Repeat("line\n", 10))
	list := files.Tools()

	if _, err := call(t, list, "read_file", `{"path":"big.txt"}`); err == nil {
		t.Error("read_file error = nil, want an error for an oversized file")
	}
	if _, err := call(t, list, "read_file", `{"path":"binary.bin"}`); err == nil {
		t.Error("read_file error = nil, want an error for a binary file")
	}

	files2 := newFiles(t, tools.FilesOptions{MaxOutputBytes: 40})
	write(t, files2.Workspace(), "many.txt", strings.Repeat("line\n", 100))
	result, err := call(t, files2.Tools(), "read_file", `{"path":"many.txt"}`)
	if err != nil {
		t.Fatalf("read_file error = %v", err)
	}
	if !result.Truncated || !strings.Contains(result.Output, "output truncated") {
		t.Errorf("result = %+v, want a truncation note", result)
	}
}

func TestListDir(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})

	result, err := call(t, files.Tools(), "list_dir", `{}`)
	if err != nil {
		t.Fatalf("list_dir error = %v", err)
	}
	for _, want := range []string{"file  go.mod", "dir   docs/", "dir   internal/"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output = %q, want it to contain %q", result.Output, want)
		}
	}
	for _, unwanted := range []string{".git", "node_modules"} {
		if strings.Contains(result.Output, unwanted) {
			t.Errorf("output = %q, want %q skipped", result.Output, unwanted)
		}
	}
}

func TestWriteFile(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.Tools()

	if _, err := call(t, list, "write_file", `{"path":"pkg/new/file.go","content":"package new\n"}`); err != nil {
		t.Fatalf("write_file error = %v", err)
	}
	path := filepath.Join(files.Workspace(), "pkg", "new", "file.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "package new\n" {
		t.Errorf("content = %q, want %q", data, "package new\n")
	}

	if _, err := call(t, list, "write_file", `{"path":"pkg/new/file.go","content":"package other\n"}`); err != nil {
		t.Fatalf("write_file error = %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if string(data) != "package other\n" {
		t.Errorf("content = %q, want the file overwritten", data)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want no leftover temporary file", len(entries))
	}
}

func TestEditFile(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.Tools()
	write(t, files.Workspace(), "twice.txt", "alpha\nbeta\nalpha\n")

	if _, err := call(t, list, "edit_file", `{"path":"go.mod","old_string":"go 1.27.0","new_string":"go 1.28.0"}`); err != nil {
		t.Fatalf("edit_file error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(files.Workspace(), "go.mod"))
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(data), "go 1.28.0") {
		t.Errorf("content = %q, want the replacement applied", data)
	}

	_, err = call(t, list, "edit_file", `{"path":"twice.txt","old_string":"alpha","new_string":"gamma"}`)
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Errorf("edit_file error = %v, want it to report the number of occurrences", err)
	}

	if _, err := call(t, list, "edit_file", `{"path":"twice.txt","old_string":"alpha","new_string":"gamma","replace_all":true}`); err != nil {
		t.Fatalf("edit_file error = %v", err)
	}
	data, err = os.ReadFile(filepath.Join(files.Workspace(), "twice.txt"))
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if strings.Contains(string(data), "alpha") {
		t.Errorf("content = %q, want every occurrence replaced", data)
	}

	tests := []struct {
		name string
		args string
	}{
		{name: "missing text", args: `{"path":"go.mod","old_string":"nowhere","new_string":"x"}`},
		{name: "identical strings", args: `{"path":"go.mod","old_string":"x","new_string":"x"}`},
		{name: "empty old string", args: `{"path":"go.mod","old_string":"","new_string":"x"}`},
		{name: "missing file", args: `{"path":"nope.txt","old_string":"a","new_string":"b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := call(t, list, "edit_file", tt.args); err == nil {
				t.Errorf("edit_file(%s) error = nil, want an error", tt.args)
			}
		})
	}
}

func TestPathsOutsideWorkspaceAreRefused(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.Tools()

	outside := []string{"../outside.txt", "/etc/passwd", "internal/../../outside.txt", "docs/../../../etc/hosts"}
	for _, name := range outside {
		args, err := json.Marshal(map[string]string{"path": name})
		if err != nil {
			t.Fatalf("marshal arguments: %v", err)
		}
		if _, err := call(t, list, "read_file", string(args)); err == nil {
			t.Errorf("read_file(%q) error = nil, want the path refused", name)
		}
		writeArgs, err := json.Marshal(map[string]string{"path": name, "content": "x"})
		if err != nil {
			t.Fatalf("marshal arguments: %v", err)
		}
		if _, err := call(t, list, "write_file", string(writeArgs)); err == nil {
			t.Errorf("write_file(%q) error = nil, want the path refused", name)
		}
	}
}

func TestSymlinkOutsideWorkspaceIsRefused(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra privileges on Windows")
	}
	files := newFiles(t, tools.FilesOptions{})

	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(files.Workspace(), "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	list := files.Tools()
	if _, err := call(t, list, "read_file", `{"path":"link.txt"}`); err == nil {
		t.Error("read_file through a symlink succeeded, want it refused")
	}
	// Writing replaces the link itself inside the workspace (the write goes
	// through a temporary file and a rename), so the target must stay intact.
	if _, err := call(t, list, "write_file", `{"path":"link.txt","content":"overwritten"}`); err != nil {
		t.Logf("write_file through a symlink was refused: %v", err)
	}
	data, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(data) != "top secret\n" {
		t.Errorf("the file outside the workspace changed to %q", data)
	}
}

func TestEditScope(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{Scope: []string{"internal"}})
	list := files.Tools()

	if _, err := call(t, list, "write_file", `{"path":"internal/app/new.go","content":"package app\n"}`); err != nil {
		t.Fatalf("write_file inside the scope error = %v", err)
	}
	_, err := call(t, list, "write_file", `{"path":"docs/readme.md","content":"text"}`)
	if err == nil || !strings.Contains(err.Error(), "edit scope") {
		t.Errorf("write_file outside the scope error = %v, want it refused by the scope", err)
	}
	if _, err := call(t, list, "read_file", `{"path":"docs/readme.md"}`); err != nil {
		t.Errorf("read_file outside the scope error = %v, want reading to stay allowed", err)
	}
	if got := files.Scope(); len(got) != 1 || got[0] != "internal" {
		t.Errorf("Scope() = %v, want [internal]", got)
	}
}

func TestInvalidArguments(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.Tools()

	tests := []struct {
		name string
		tool string
		args string
	}{
		{name: "not json", tool: "read_file", args: `not json`},
		{name: "unknown field", tool: "read_file", args: `{"path":"go.mod","depth":2}`},
		{name: "missing path", tool: "read_file", args: `{}`},
		{name: "empty path", tool: "write_file", args: `{"path":"  ","content":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := call(t, list, tt.tool, tt.args); err == nil {
				t.Errorf("%s(%s) error = nil, want an error", tt.tool, tt.args)
			}
		})
	}
}

func TestNewFilesRejectsBadInput(t *testing.T) {
	t.Parallel()

	if _, err := tools.NewFiles("", tools.FilesOptions{}); err == nil {
		t.Error("NewFiles(\"\") error = nil, want an error")
	}
	if _, err := tools.NewFiles(filepath.Join(t.TempDir(), "missing"), tools.FilesOptions{}); err == nil {
		t.Error("NewFiles(missing) error = nil, want an error")
	}
	if _, err := tools.NewFiles(t.TempDir(), tools.FilesOptions{Scope: []string{"../outside"}}); err == nil {
		t.Error("NewFiles() error = nil, want an error for a scope outside the workspace")
	}
}
