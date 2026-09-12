package tools_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/sandbox"
	"github.com/simplesys/locallm/internal/tools"
)

// plainRunner runs commands without isolation, so the shell tool can be
// tested without depending on the platform sandbox.
type plainRunner struct {
	level sandbox.Level
	dir   string
}

func (r plainRunner) Level() sandbox.Level {
	if r.level == "" {
		return sandbox.LevelNone
	}
	return r.level
}

func (r plainRunner) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.dir
	return cmd
}

func newShell(t *testing.T, opts tools.ShellOptions) tools.Tool {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test commands are POSIX shell commands")
	}
	if opts.Workspace == "" {
		opts.Workspace = t.TempDir()
	}
	if opts.Sandbox == nil {
		opts.Sandbox = plainRunner{dir: opts.Workspace}
	}
	tool, err := tools.NewShell(opts)
	if err != nil {
		t.Fatalf("NewShell() error = %v", err)
	}
	return tool
}

func runTool(t *testing.T, tool tools.Tool, args string) (tools.Result, error) {
	t.Helper()
	return tool.Run(t.Context(), json.RawMessage(args))
}

func TestShellRunsCommands(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{})
	result, err := runTool(t, tool, `{"command":"echo ok"}`)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if !strings.Contains(result.Output, "exit code: 0") || !strings.Contains(result.Output, "ok") {
		t.Errorf("output = %q, want exit code 0 and the command output", result.Output)
	}
	if !tool.Mutating {
		t.Error("Mutating = false, want the shell tool to require approval")
	}
}

func TestShellReportsFailure(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{})
	result, err := runTool(t, tool, `{"command":"echo problem 1>&2; exit 3"}`)
	if err != nil {
		t.Fatalf("shell error = %v, want a failing command to be a normal result", err)
	}
	if !strings.Contains(result.Output, "exit code: 3") {
		t.Errorf("output = %q, want exit code 3", result.Output)
	}
	if !strings.Contains(result.Output, "--- stderr") || !strings.Contains(result.Output, "problem") {
		t.Errorf("output = %q, want the stderr section", result.Output)
	}
}

func TestShellTimeout(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{Timeout: 50 * time.Millisecond})
	start := time.Now()
	result, err := runTool(t, tool, `{"command":"sleep 5"}`)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shell took %s, want it to kill the command quickly", elapsed)
	}
	if !strings.Contains(result.Output, "killed after") {
		t.Errorf("output = %q, want a note that the command was killed", result.Output)
	}
}

func TestShellCapsRequestedTimeout(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{Timeout: 50 * time.Millisecond})
	result, err := runTool(t, tool, `{"command":"sleep 5","timeout_seconds":60}`)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if !strings.Contains(result.Output, "killed after 50ms") {
		t.Errorf("output = %q, want the requested timeout capped to the configured one", result.Output)
	}
}

func TestShellTruncatesOutput(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{MaxOutputBytes: 1000})
	result, err := runTool(t, tool, `{"command":"head -c 200000 /dev/zero | tr '\\0' 'a'"}`)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want the output to be capped")
	}
	if len(result.Output) > 4000 {
		t.Errorf("len(output) = %d, want it close to the limit", len(result.Output))
	}
}

func TestShellRefusesGitHistoryCommands(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{Deny: []string{"rm -rf"}})
	refused := []string{
		`{"command":"git commit -m x"}`,
		`{"command":"GIT PUSH"}`,
		`{"command":"  git   push  origin main"}`,
		`{"command":"git reset --hard HEAD~1"}`,
		`{"command":"rm -rf /"}`,
	}
	for _, args := range refused {
		if _, err := runTool(t, tool, args); err == nil {
			t.Errorf("shell(%s) error = nil, want the command refused", args)
		}
	}
	if _, err := runTool(t, tool, `{"command":"git status --porcelain"}`); err != nil {
		t.Errorf("shell(git status) error = %v, want read-only git commands to run", err)
	}
}

func TestShellRejectsEmptyCommand(t *testing.T) {
	t.Parallel()

	tool := newShell(t, tools.ShellOptions{})
	for _, args := range []string{`{"command":""}`, `{"command":"   "}`, `{}`} {
		if _, err := runTool(t, tool, args); err == nil {
			t.Errorf("shell(%s) error = nil, want an error", args)
		}
	}
}

func TestShellReportsSandboxLevel(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	tool := newShell(t, tools.ShellOptions{
		Workspace: workspace,
		Sandbox:   plainRunner{level: sandbox.LevelSeatbelt, dir: workspace},
	})
	result, err := runTool(t, tool, `{"command":"echo ok"}`)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if !strings.HasPrefix(result.Output, "sandbox: seatbelt\n") {
		t.Errorf("output = %q, want the sandbox level first", result.Output)
	}
}

func TestNewShellRequiresOptions(t *testing.T) {
	t.Parallel()

	if _, err := tools.NewShell(tools.ShellOptions{Sandbox: plainRunner{}}); err == nil {
		t.Error("NewShell() error = nil, want an error without a workspace")
	}
	if _, err := tools.NewShell(tools.ShellOptions{Workspace: t.TempDir()}); err == nil {
		t.Error("NewShell() error = nil, want an error without a sandbox")
	}
}

func TestDefaultShell(t *testing.T) {
	t.Parallel()

	shell := tools.DefaultShell()
	if len(shell) < 2 {
		t.Fatalf("DefaultShell() = %v, want an interpreter and a flag", shell)
	}
	if runtime.GOOS == "windows" && shell[0] != "powershell" {
		t.Errorf("DefaultShell() = %v, want powershell on Windows", shell)
	}
	if runtime.GOOS != "windows" && shell[0] != "sh" {
		t.Errorf("DefaultShell() = %v, want sh", shell)
	}
}
