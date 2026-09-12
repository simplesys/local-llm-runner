package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/simplesys/locallm/internal/sandbox"
)

// Defaults of the shell tool.
const (
	defaultShellTimeout = 2 * time.Minute
	maxShellOutput      = 32 << 10
)

// deniedCommands never run: history and remote repository operations belong
// to the user, not to the agent (FR-10).
var deniedCommands = []string{
	"git commit", "git push", "git reset --hard", "git rebase",
	"git checkout -b", "git branch", "git tag", "git remote",
}

// commandRunner is the part of the sandbox the shell tool needs.
type commandRunner interface {
	Level() sandbox.Level
	Command(ctx context.Context, name string, args ...string) *exec.Cmd
}

// ShellOptions configures the shell tool. Zero values mean the defaults named
// in the comments.
type ShellOptions struct {
	// Workspace is the working directory; required.
	Workspace string
	// Sandbox builds the command; required.
	Sandbox commandRunner
	// Timeout bounds one command (2 minutes).
	Timeout time.Duration
	// MaxOutputBytes truncates each captured stream (32 KiB).
	MaxOutputBytes int
	// Shell is the interpreter and its flags (platform default).
	Shell []string
	// Deny lists extra command prefixes to refuse.
	Deny []string
}

// DefaultShell returns the interpreter used to run commands on this platform.
func DefaultShell() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell", "-NoProfile", "-Command"}
	}
	return []string{"sh", "-c"}
}

// shellArgs are the arguments of the shell tool.
type shellArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// NewShell builds the shell tool.
func NewShell(opts ShellOptions) (Tool, error) {
	if strings.TrimSpace(opts.Workspace) == "" {
		return Tool{}, errors.New("workspace must not be empty")
	}
	if opts.Sandbox == nil {
		return Tool{}, errors.New("sandbox must not be nil")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultShellTimeout
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = maxShellOutput
	}
	if len(opts.Shell) == 0 {
		opts.Shell = DefaultShell()
	}

	return Tool{
		Name: "shell",
		Description: "Run a shell command in the workspace and return its exit code, stdout and stderr. " +
			"Use it for builds, tests and linters. Commands that change git history or a remote are refused.",
		Mutating: true,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Command line to run"},
    "timeout_seconds": {"type": "integer", "description": "Time limit for this command"}
  },
  "required": ["command"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args shellArgs
			if err := decodeArgs(raw, &args); err != nil {
				return Result{}, err
			}
			return runShell(ctx, opts, args)
		},
	}, nil
}

func runShell(ctx context.Context, opts ShellOptions, args shellArgs) (Result, error) {
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return Result{}, errors.New("command must not be empty")
	}
	if denied := deniedPrefix(command, opts.Deny); denied != "" {
		return Result{}, fmt.Errorf("command %q is refused: git history and remote operations belong to the user", denied)
	}

	timeout := opts.Timeout
	if args.TimeoutSeconds > 0 {
		requested := time.Duration(args.TimeoutSeconds) * time.Second
		timeout = min(requested, opts.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interpreterArgs := append(append([]string(nil), opts.Shell[1:]...), command)
	cmd := opts.Sandbox.Command(ctx, opts.Shell[0], interpreterArgs...)
	cmd.Env = append(os.Environ(), "LOCALLM=1")
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedBuffer{buf: &stdout, limit: opts.MaxOutputBytes}
	cmd.Stderr = &boundedBuffer{buf: &stderr, limit: opts.MaxOutputBytes}

	runErr := cmd.Run()
	var exitError *exec.ExitError
	switch {
	case runErr == nil:
		// exit code 0
	case errors.As(runErr, &exitError):
		// the command ran and failed, which the model needs to see
	default:
		return Result{}, fmt.Errorf("run command %q: %w", command, runErr)
	}
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	var out strings.Builder
	if level := opts.Sandbox.Level(); level != sandbox.LevelNone {
		fmt.Fprintf(&out, "sandbox: %s\n", level)
	}
	fmt.Fprintf(&out, "exit code: %d\n", exitCode)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		fmt.Fprintf(&out, "killed after %s\n", timeout)
	}
	if stdout.Len() > 0 {
		fmt.Fprintf(&out, "--- stdout\n%s\n", strings.TrimRight(stdout.String(), "\n"))
	}
	if stderr.Len() > 0 {
		fmt.Fprintf(&out, "--- stderr\n%s\n", strings.TrimRight(stderr.String(), "\n"))
	}

	// A command that fails or times out is a normal result: the model has to
	// see the exit code and the output. A cancellation from the caller is not.
	if errors.Is(ctx.Err(), context.Canceled) {
		return Result{}, fmt.Errorf("run command %q: %w", command, ctx.Err())
	}
	truncated := stdout.Len() >= opts.MaxOutputBytes || stderr.Len() >= opts.MaxOutputBytes
	return Result{Output: out.String(), Truncated: truncated}, nil
}

// deniedPrefix reports the refused prefix a command starts with, if any.
func deniedPrefix(command string, extra []string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(command)), " ")
	for _, prefix := range append(append([]string(nil), deniedCommands...), extra...) {
		normalizedPrefix := strings.Join(strings.Fields(strings.ToLower(prefix)), " ")
		if normalized == normalizedPrefix || strings.HasPrefix(normalized, normalizedPrefix+" ") {
			return prefix
		}
	}
	return ""
}

// boundedBuffer keeps at most limit bytes and drops the rest, so that a
// runaway command cannot exhaust memory or the context window.
type boundedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}
