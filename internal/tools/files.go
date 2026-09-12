package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Default limits of the file tools.
const (
	defaultMaxReadBytes   = 256 << 10
	defaultMaxOutputBytes = 32 << 10
	maxDirEntries         = 500
	binarySniffBytes      = 8 << 10
)

// skipDirs are directories that never carry code the model should read.
var skipDirs = []string{".git", "node_modules", "vendor", "dist", "bin", ".idea", ".vscode", "target", "build", ".venv"}

// Files provides the file tools of one workspace.
type Files struct {
	root           *os.Root
	workspace      string
	maxReadBytes   int64
	maxOutputBytes int
	scope          []string
}

// FilesOptions configures the file tools. Zero values mean the defaults named
// in the comments.
type FilesOptions struct {
	// MaxReadBytes refuses to read files bigger than this (256 KiB).
	MaxReadBytes int64
	// MaxOutputBytes truncates tool output (32 KiB).
	MaxOutputBytes int
	// Scope lists workspace-relative prefixes the agent may modify; empty
	// means the whole workspace.
	Scope []string
}

// NewFiles opens the workspace through os.OpenRoot and builds the file tools.
// The caller closes it when the session ends.
func NewFiles(workspace string, opts FilesOptions) (*Files, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("workspace must not be empty")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace %s: %w", workspace, err)
	}
	files := &Files{
		root:           root,
		workspace:      workspace,
		maxReadBytes:   opts.MaxReadBytes,
		maxOutputBytes: opts.MaxOutputBytes,
	}
	if files.maxReadBytes <= 0 {
		files.maxReadBytes = defaultMaxReadBytes
	}
	if files.maxOutputBytes <= 0 {
		files.maxOutputBytes = defaultMaxOutputBytes
	}
	for _, prefix := range opts.Scope {
		clean, err := cleanPath(prefix)
		if err != nil {
			return nil, fmt.Errorf("edit scope %q: %w", prefix, err)
		}
		files.scope = append(files.scope, clean)
	}
	return files, nil
}

// Close releases the workspace root.
func (f *Files) Close() error {
	if err := f.root.Close(); err != nil {
		return fmt.Errorf("close workspace %s: %w", f.workspace, err)
	}
	return nil
}

// Workspace returns the directory the tools are confined to.
func (f *Files) Workspace() string { return f.workspace }

// Scope returns the paths the agent may modify; empty means everything inside
// the workspace.
func (f *Files) Scope() []string { return slices.Clone(f.scope) }

// Tools returns read_file, list_dir, write_file and edit_file.
func (f *Files) Tools() []Tool {
	return []Tool{f.readFileTool(), f.listDirTool(), f.writeFileTool(), f.editFileTool()}
}

// cleanPath validates a path that came from the model and returns it in slash
// form, relative to the workspace.
func cleanPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ".", nil
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		return "", errors.New("want a path relative to the workspace")
	}
	if volume := filepath.VolumeName(trimmed); volume != "" {
		return "", errors.New("want a path relative to the workspace")
	}
	clean := path.Clean(filepath.ToSlash(trimmed))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path leaves the workspace")
	}
	return clean, nil
}

// checkScope reports whether the path may be modified.
func (f *Files) checkScope(clean string) error {
	if len(f.scope) == 0 {
		return nil
	}
	for _, prefix := range f.scope {
		if prefix == "." || clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("path %s is outside the edit scope (%s)", clean, strings.Join(f.scope, ", "))
}

// readFileArgs are the arguments of read_file.
type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (f *Files) readFileTool() Tool {
	return Tool{
		Name: "read_file",
		Description: "Read a text file from the workspace. Returns the content with line numbers. " +
			"Use offset (1-based first line) and limit to read part of a large file.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path relative to the workspace"},
    "offset": {"type": "integer", "description": "First line to return, 1-based"},
    "limit": {"type": "integer", "description": "How many lines to return"}
  },
  "required": ["path"]
}`),
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var args readFileArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Result{}, errors.New("path must not be empty")
			}
			clean, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			return f.readFile(clean, args.Offset, args.Limit)
		},
	}
}

func (f *Files) readFile(clean string, offset, limit int) (Result, error) {
	info, err := f.root.Stat(clean)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", clean, err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory, use list_dir", clean)
	}
	if info.Size() > f.maxReadBytes {
		return Result{}, fmt.Errorf("%s is %d bytes, the limit is %d: read it in parts with grep or offset and limit",
			clean, info.Size(), f.maxReadBytes)
	}

	file, err := f.root.Open(clean)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", clean, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, f.maxReadBytes))
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", clean, err)
	}
	if isBinary(data) {
		return Result{}, fmt.Errorf("%s looks like a binary file", clean)
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	start := max(offset, 1)
	if start > len(lines) {
		return Result{Output: fmt.Sprintf("%s has %d lines, offset %d is past the end", clean, len(lines), start)}, nil
	}
	end := len(lines)
	if limit > 0 && start-1+limit < end {
		end = start - 1 + limit
	}

	var out strings.Builder
	for index := start - 1; index < end; index++ {
		fmt.Fprintf(&out, "%d\t%s\n", index+1, lines[index])
	}
	output, truncated := truncate(out.String(), f.maxOutputBytes)
	return Result{Output: output, Truncated: truncated}, nil
}

// isBinary reports whether the data looks like a binary file.
func isBinary(data []byte) bool {
	head := data
	if len(head) > binarySniffBytes {
		head = head[:binarySniffBytes]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}

// listDirArgs are the arguments of list_dir.
type listDirArgs struct {
	Path string `json:"path"`
}

func (f *Files) listDirTool() Tool {
	return Tool{
		Name:        "list_dir",
		Description: "List the entries of a directory in the workspace. Build and dependency directories are skipped.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory relative to the workspace, default \".\""}
  },
  "required": []
}`),
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var args listDirArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			clean, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			return f.listDir(clean)
		},
	}
}

func (f *Files) listDir(clean string) (Result, error) {
	entries, err := fs.ReadDir(f.root.FS(), clean)
	if err != nil {
		return Result{}, fmt.Errorf("read directory %s: %w", clean, err)
	}

	var out strings.Builder
	listed := 0
	truncated := false
	for _, entry := range entries {
		if entry.IsDir() && slices.Contains(skipDirs, entry.Name()) {
			continue
		}
		if listed == maxDirEntries {
			fmt.Fprintf(&out, "... %d more entries omitted\n", len(entries)-listed)
			truncated = true
			break
		}
		listed++
		if entry.IsDir() {
			fmt.Fprintf(&out, "dir   %s/\n", entry.Name())
			continue
		}
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(&out, "file  %s\n", entry.Name())
			continue
		}
		fmt.Fprintf(&out, "file  %s (%d bytes)\n", entry.Name(), info.Size())
	}
	if listed == 0 && !truncated {
		return Result{Output: clean + " is empty"}, nil
	}
	output, cut := truncate(out.String(), f.maxOutputBytes)
	return Result{Output: output, Truncated: truncated || cut}, nil
}

// writeFileArgs are the arguments of write_file.
type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (f *Files) writeFileTool() Tool {
	return Tool{
		Name:        "write_file",
		Description: "Create a file or replace its whole content. Missing parent directories are created.",
		Mutating:    true,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path relative to the workspace"},
    "content": {"type": "string", "description": "The whole new content of the file"}
  },
  "required": ["path", "content"]
}`),
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var args writeFileArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Result{}, errors.New("path must not be empty")
			}
			clean, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			if err := f.checkScope(clean); err != nil {
				return Result{}, err
			}
			if err := f.writeFile(clean, []byte(args.Content)); err != nil {
				return Result{}, err
			}
			return Result{Output: fmt.Sprintf("wrote %s (%d bytes)", clean, len(args.Content))}, nil
		},
	}
}

// writeFile stores data through a temporary file in the same directory, so a
// failed write never leaves a half-written file behind.
func (f *Files) writeFile(clean string, data []byte) error {
	if parent := path.Dir(clean); parent != "." {
		if err := f.root.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", parent, err)
		}
	}
	perm := os.FileMode(0o644)
	if info, err := f.root.Stat(clean); err == nil {
		perm = info.Mode().Perm()
	}

	temp := clean + ".locallm.tmp"
	if err := f.root.WriteFile(temp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", clean, err)
	}
	if err := f.root.Rename(temp, clean); err != nil {
		return errors.Join(fmt.Errorf("replace %s: %w", clean, err), f.root.Remove(temp))
	}
	return nil
}

// editFileArgs are the arguments of edit_file.
type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (f *Files) editFileTool() Tool {
	return Tool{
		Name: "edit_file",
		Description: "Replace an exact piece of text in a file. Without replace_all the text must occur exactly once, " +
			"so include enough surrounding lines to make it unique.",
		Mutating: true,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path relative to the workspace"},
    "old_string": {"type": "string", "description": "Exact text to replace"},
    "new_string": {"type": "string", "description": "Replacement text"},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence"}
  },
  "required": ["path", "old_string", "new_string"]
}`),
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var args editFileArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Result{}, errors.New("path must not be empty")
			}
			if args.OldString == args.NewString {
				return Result{}, errors.New("old_string and new_string are identical")
			}
			if args.OldString == "" {
				return Result{}, errors.New("old_string must not be empty, use write_file to create a file")
			}
			clean, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			if err := f.checkScope(clean); err != nil {
				return Result{}, err
			}
			return f.editFile(clean, args)
		},
	}
}

func (f *Files) editFile(clean string, args editFileArgs) (Result, error) {
	info, err := f.root.Stat(clean)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", clean, err)
	}
	if info.Size() > f.maxReadBytes {
		return Result{}, fmt.Errorf("%s is %d bytes, the limit is %d", clean, info.Size(), f.maxReadBytes)
	}
	data, err := f.root.ReadFile(clean)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", clean, err)
	}

	content := string(data)
	occurrences := strings.Count(content, args.OldString)
	switch {
	case occurrences == 0:
		return Result{}, fmt.Errorf("old_string not found in %s", clean)
	case occurrences > 1 && !args.ReplaceAll:
		return Result{}, fmt.Errorf("old_string occurs %d times in %s: add surrounding context or set replace_all",
			occurrences, clean)
	}

	replacements := 1
	if args.ReplaceAll {
		replacements = occurrences
	}
	updated := strings.Replace(content, args.OldString, args.NewString, replacements)
	if err := f.writeFile(clean, []byte(updated)); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("edited %s (%d replacement(s))", clean, replacements)}, nil
}
