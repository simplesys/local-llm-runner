package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/tools"
)

func TestGlob(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	write(t, files.Workspace(), "internal/app/app_test.go", "package app\n")
	list := files.SearchTools(tools.SearchOptions{})

	tests := []struct {
		name    string
		args    string
		want    []string
		unwant  []string
		wantAny bool
	}{
		{name: "by extension", args: `{"pattern":"*.go"}`, want: []string{"main.go", "internal/app/app.go"}},
		{name: "recursive tests", args: `{"pattern":"**/*_test.go"}`, want: []string{"internal/app/app_test.go"}, unwant: []string{"main.go"}},
		{name: "path pattern", args: `{"pattern":"internal/app/*.go","path":"."}`, want: []string{"internal/app/app.go"}},
		{name: "inside a directory", args: `{"pattern":"*.md","path":"docs"}`, want: []string{"docs/readme.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := call(t, list, "glob", tt.args)
			if err != nil {
				t.Fatalf("glob error = %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(result.Output, want) {
					t.Errorf("output = %q, want it to contain %q", result.Output, want)
				}
			}
			for _, unwanted := range tt.unwant {
				if strings.Contains(result.Output, unwanted) {
					t.Errorf("output = %q, want it without %q", result.Output, unwanted)
				}
			}
		})
	}
}

func TestGlobSkipsAndSorts(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.SearchTools(tools.SearchOptions{})

	result, err := call(t, list, "glob", `{"pattern":"*"}`)
	if err != nil {
		t.Fatalf("glob error = %v", err)
	}
	for _, unwanted := range []string{".git/", "node_modules/"} {
		if strings.Contains(result.Output, unwanted) {
			t.Errorf("output = %q, want %q skipped", result.Output, unwanted)
		}
	}

	newest := filepath.Join(files.Workspace(), "docs", "readme.md")
	if err := os.Chtimes(newest, time.Now(), time.Now()); err != nil {
		t.Fatalf("touch file: %v", err)
	}
	older := filepath.Join(files.Workspace(), "main.go")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("touch file: %v", err)
	}

	result, err = call(t, list, "glob", `{"pattern":"**/*"}`)
	if err != nil {
		t.Fatalf("glob error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	readmeIndex, mainIndex := -1, -1
	for index, line := range lines {
		switch line {
		case "docs/readme.md":
			readmeIndex = index
		case "main.go":
			mainIndex = index
		}
	}
	if readmeIndex == -1 || mainIndex == -1 || readmeIndex > mainIndex {
		t.Errorf("output =\n%s\nwant the newest file first", result.Output)
	}
}

func TestGlobLimit(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	for index := range 30 {
		write(t, files.Workspace(), "gen/file"+string(rune('a'+index%26))+string(rune('a'+index/26))+".txt", "x")
	}
	list := files.SearchTools(tools.SearchOptions{MaxMatches: 5})

	result, err := call(t, list, "glob", `{"pattern":"**/*.txt"}`)
	if err != nil {
		t.Fatalf("glob error = %v", err)
	}
	if !result.Truncated || !strings.Contains(result.Output, "limit 5") {
		t.Errorf("result = %+v, want a truncation note", result)
	}
}

func TestGlobNoMatches(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.SearchTools(tools.SearchOptions{})

	result, err := call(t, list, "glob", `{"pattern":"*.nothing"}`)
	if err != nil {
		t.Fatalf("glob error = %v", err)
	}
	if result.Output != "no matches" {
		t.Errorf("output = %q, want %q", result.Output, "no matches")
	}
}

func TestGrep(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.SearchTools(tools.SearchOptions{})

	result, err := call(t, list, "grep", `{"pattern":"func main"}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if !strings.Contains(result.Output, "main.go:3: func main()") {
		t.Errorf("output = %q, want path, line number and text", result.Output)
	}

	result, err = call(t, list, "grep", `{"pattern":"func main","context_lines":2}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if !strings.Contains(result.Output, "main.go:1- package main") {
		t.Errorf("output = %q, want context lines", result.Output)
	}

	result, err = call(t, list, "grep", `{"pattern":"PACKAGE","case_insensitive":true}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if !strings.Contains(result.Output, "package") {
		t.Errorf("output = %q, want case-insensitive matches", result.Output)
	}

	result, err = call(t, list, "grep", `{"pattern":"package","glob":"*.md"}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if result.Output != "no matches" {
		t.Errorf("output = %q, want the glob filter to exclude Go files", result.Output)
	}
}

func TestGrepSkipsUnreadableContent(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	write(t, files.Workspace(), "blob.bin", "needle\x00needle")
	write(t, files.Workspace(), "big.txt", strings.Repeat("needle\n", 1000))
	list := files.SearchTools(tools.SearchOptions{MaxFileBytes: 100})

	result, err := call(t, list, "grep", `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if result.Output != "no matches" {
		t.Errorf("output = %q, want binary and oversized files skipped", result.Output)
	}
}

func TestGrepLimitAndLongLines(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	write(t, files.Workspace(), "many.txt", strings.Repeat("needle\n", 300))
	write(t, files.Workspace(), "long.txt", strings.Repeat("x", 1000)+"needle\n")
	list := files.SearchTools(tools.SearchOptions{MaxMatches: 10})

	result, err := call(t, list, "grep", `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("grep error = %v", err)
	}
	if !result.Truncated || !strings.Contains(result.Output, "limit 10") {
		t.Errorf("result = %+v, want a truncation note", result)
	}
	for _, line := range strings.Split(result.Output, "\n") {
		if len(line) > 400 {
			t.Errorf("line of %d bytes, want long lines clipped", len(line))
		}
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	list := files.SearchTools(tools.SearchOptions{})

	if _, err := call(t, list, "grep", `{"pattern":"("}`); err == nil {
		t.Error("grep error = nil, want an error for an invalid expression")
	}
	if _, err := call(t, list, "grep", `{"pattern":"x","path":"../"}`); err == nil {
		t.Error("grep error = nil, want the path outside the workspace refused")
	}
	if _, err := call(t, list, "glob", `{"pattern":"x","path":"/etc"}`); err == nil {
		t.Error("glob error = nil, want the absolute path refused")
	}
}

func TestSearchRespectsCanceledContext(t *testing.T) {
	t.Parallel()

	files := newFiles(t, tools.FilesOptions{})
	for index := range 200 {
		write(t, files.Workspace(), "gen/"+string(rune('a'+index%26))+"/f"+string(rune('a'+index%26))+".txt", "needle\n")
	}
	list := files.SearchTools(tools.SearchOptions{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, name := range []string{"glob", "grep"} {
		for _, tool := range list {
			if tool.Name != name {
				continue
			}
			if _, err := tool.Run(ctx, json.RawMessage(`{"pattern":"needle"}`)); err == nil {
				t.Errorf("%s error = nil, want a context error", name)
			}
		}
	}
}
