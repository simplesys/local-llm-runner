package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Default limits of the search tools. A file tree of a few thousand files is
// tens of thousands of tokens, so results have to be capped.
const (
	defaultMaxMatches   = 200
	defaultMaxFileBytes = 1 << 20
	maxGrepLineLength   = 300
	maxContextLines     = 5
	scannerMaxBuffer    = 1 << 20
)

// SearchOptions configures the search tools. Zero values mean the defaults
// named in the comments.
type SearchOptions struct {
	// MaxMatches stops the search after this many matches (200).
	MaxMatches int
	// MaxFileBytes skips bigger files when grepping (1 MiB).
	MaxFileBytes int64
	// SkipDirs overrides the default list of skipped directories.
	SkipDirs []string
}

// withDefaults fills the options that were left unset.
func (o SearchOptions) withDefaults() SearchOptions {
	if o.MaxMatches <= 0 {
		o.MaxMatches = defaultMaxMatches
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = defaultMaxFileBytes
	}
	if len(o.SkipDirs) == 0 {
		o.SkipDirs = skipDirs
	}
	return o
}

// SearchTools returns the glob and grep tools of the workspace.
func (f *Files) SearchTools(opts SearchOptions) []Tool {
	opts = opts.withDefaults()
	return []Tool{f.globTool(opts), f.grepTool(opts)}
}

// globArgs are the arguments of glob.
type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (f *Files) globTool(opts SearchOptions) Tool {
	return Tool{
		Name: "glob",
		Description: "Find files by name pattern, newest first. The pattern matches the file name, " +
			"or the path relative to the search directory when it contains a slash. A \"**/\" prefix means any depth.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Name pattern, for example *_test.go or **/*.md"},
    "path": {"type": "string", "description": "Directory to search in, default \".\""}
  },
  "required": ["pattern"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args globArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Pattern) == "" {
				return Result{}, errors.New("pattern must not be empty")
			}
			root, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			return f.glob(ctx, root, args.Pattern, opts)
		},
	}
}

// match reports whether the pattern matches the entry.
func match(pattern, relative, name string) (bool, error) {
	if trimmed, found := strings.CutPrefix(pattern, "**/"); found {
		return path.Match(trimmed, name)
	}
	if strings.Contains(pattern, "/") {
		return path.Match(pattern, relative)
	}
	return path.Match(pattern, name)
}

// found is one file matched by glob.
type found struct {
	path    string
	modTime time.Time
}

func (f *Files) glob(ctx context.Context, root, pattern string, opts SearchOptions) (Result, error) {
	if _, err := match(pattern, "probe", "probe"); err != nil {
		return Result{}, fmt.Errorf("pattern %q: %w", pattern, err)
	}

	var matches []found
	truncated := false
	walkErr := fs.WalkDir(f.root.FS(), root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry must not stop the walk
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name != root && slices.Contains(opts.SkipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(name, root), "/")
		ok, err := match(pattern, relative, entry.Name())
		if err != nil || !ok {
			return nil //nolint:nilerr // a bad pattern is reported by the probe above
		}
		if len(matches) >= opts.MaxMatches {
			truncated = true
			return fs.SkipAll
		}
		info, err := entry.Info()
		if err != nil {
			matches = append(matches, found{path: name})
			return nil
		}
		matches = append(matches, found{path: name, modTime: info.ModTime()})
		return nil
	})
	if walkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	if len(matches) == 0 {
		return Result{Output: "no matches"}, nil
	}

	sort.SliceStable(matches, func(i, j int) bool { return matches[i].modTime.After(matches[j].modTime) })
	var out strings.Builder
	for _, item := range matches {
		out.WriteString(item.path)
		out.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&out, "... more matches omitted (limit %d)\n", opts.MaxMatches)
	}
	output, cut := truncate(out.String(), f.maxOutputBytes)
	return Result{Output: output, Truncated: truncated || cut}, nil
}

// grepArgs are the arguments of grep.
type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"case_insensitive"`
	ContextLines    int    `json:"context_lines"`
}

func (f *Files) grepTool(opts SearchOptions) Tool {
	return Tool{
		Name: "grep",
		Description: "Search file contents with a Go regular expression. Reports path:line: text for every match. " +
			"Binary and very large files are skipped.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Go regular expression"},
    "path": {"type": "string", "description": "Directory or file to search in, default \".\""},
    "glob": {"type": "string", "description": "Only search files whose name matches this pattern"},
    "case_insensitive": {"type": "boolean", "description": "Ignore case"},
    "context_lines": {"type": "integer", "description": "Lines of context around each match, 0 to 5"}
  },
  "required": ["pattern"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args grepArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Pattern) == "" {
				return Result{}, errors.New("pattern must not be empty")
			}
			root, err := cleanPath(args.Path)
			if err != nil {
				return Result{}, fmt.Errorf("path %q: %w", args.Path, err)
			}
			return f.grep(ctx, root, args, opts)
		},
	}
}

func (f *Files) grep(ctx context.Context, root string, args grepArgs, opts SearchOptions) (Result, error) {
	expression := args.Pattern
	if args.CaseInsensitive {
		expression = "(?i)" + expression
	}
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return Result{}, fmt.Errorf("pattern %q: %w", args.Pattern, err)
	}
	contextLines := min(max(args.ContextLines, 0), maxContextLines)

	var out strings.Builder
	matches := 0
	truncated := false

	walkErr := fs.WalkDir(f.root.FS(), root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name != root && slices.Contains(opts.SkipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if matches >= opts.MaxMatches {
			truncated = true
			return fs.SkipAll
		}
		if args.Glob != "" {
			ok, err := match(args.Glob, name, entry.Name())
			if err != nil || !ok {
				return nil //nolint:nilerr // an invalid file glob simply matches nothing
			}
		}
		info, err := entry.Info()
		if err != nil || info.Size() > opts.MaxFileBytes {
			return nil //nolint:nilerr // oversized or unreadable files are skipped on purpose
		}
		found, err := f.grepFile(name, pattern, contextLines, opts.MaxMatches-matches, &out)
		if err != nil {
			return nil //nolint:nilerr // one unreadable file must not fail the search
		}
		matches += found
		return nil
	})
	if walkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	if matches == 0 {
		return Result{Output: "no matches"}, nil
	}
	if truncated || matches >= opts.MaxMatches {
		fmt.Fprintf(&out, "... more matches omitted (limit %d)\n", opts.MaxMatches)
		truncated = true
	}
	output, cut := truncate(out.String(), f.maxOutputBytes)
	return Result{Output: output, Truncated: truncated || cut}, nil
}

// grepFile searches one file and appends its matches to out.
func (f *Files) grepFile(name string, pattern *regexp.Regexp, contextLines, budget int, out *strings.Builder) (int, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", name, err)
	}
	defer file.Close()

	head := make([]byte, binarySniffBytes)
	read, err := file.Read(head)
	if err != nil && read == 0 {
		return 0, nil
	}
	if isBinary(head[:read]) {
		return 0, nil
	}
	if _, err := file.Seek(0, 0); err != nil {
		return 0, fmt.Errorf("rewind %s: %w", name, err)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), scannerMaxBuffer)

	var before []string
	matches := 0
	after := 0
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := clipLine(scanner.Text())
		switch {
		case pattern.MatchString(line):
			for offset, text := range before {
				fmt.Fprintf(out, "%s:%d- %s\n", name, lineNumber-len(before)+offset, text)
			}
			before = before[:0]
			fmt.Fprintf(out, "%s:%d: %s\n", name, lineNumber, line)
			matches++
			after = contextLines
			if matches >= budget {
				return matches, nil
			}
		case after > 0:
			fmt.Fprintf(out, "%s:%d- %s\n", name, lineNumber, line)
			after--
		case contextLines > 0:
			before = append(before, line)
			if len(before) > contextLines {
				before = before[1:]
			}
		}
	}
	return matches, nil
}

// clipLine shortens a long line so that one minified file cannot flood the
// output.
func clipLine(line string) string {
	if len(line) <= maxGrepLineLength {
		return line
	}
	return line[:maxGrepLineLength] + "..."
}
