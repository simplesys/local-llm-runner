package lmstudio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrCLINotFound reports that the lms executable could not be located.
var ErrCLINotFound = errors.New("lms executable not found")

// defaultCommandTimeout bounds a single lms invocation. Loading a large model
// can take minutes, so the limit is generous.
const defaultCommandTimeout = 10 * time.Minute

// maxCapturedOutput caps how much of the lms output is kept for error
// messages.
const maxCapturedOutput = 64 << 10

// Locator finds the lms executable. The zero value inspects the real
// environment; the function fields exist so tests can substitute it.
type Locator struct {
	LookPath func(file string) (string, error)
	HomeDir  func() (string, error)
	Stat     func(name string) (os.FileInfo, error)
	Getenv   func(key string) string
	GOOS     string
}

// Find returns the path to the lms executable, looking at PATH first and then
// at the locations LM Studio installs it to. It wraps ErrCLINotFound when
// nothing is found.
func (l Locator) Find() (string, error) {
	lookPath := l.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	goos := l.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	name := "lms"
	if goos == "windows" {
		name = "lms.exe"
	}
	if path, err := lookPath(name); err == nil {
		return path, nil
	}

	stat := l.Stat
	if stat == nil {
		stat = os.Stat
	}
	for _, candidate := range l.candidates(goos) {
		if info, err := stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: install LM Studio or add lms to PATH", ErrCLINotFound)
}

// candidates lists the platform-specific install locations of lms.
func (l Locator) candidates(goos string) []string {
	homeDir := l.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	getenv := l.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	var roots []string
	if custom := getenv("LMSTUDIO_HOME"); custom != "" {
		roots = append(roots, custom)
	}
	home, err := homeDir()
	if err == nil && home != "" {
		roots = append(roots, home)
	}

	var paths []string
	for _, root := range roots {
		if goos == "windows" {
			paths = append(paths,
				filepath.Join(root, ".lmstudio", "bin", "lms.exe"),
				filepath.Join(root, "AppData", "Local", "LM Studio", "lms.exe"),
			)
			continue
		}
		paths = append(paths,
			filepath.Join(root, ".lmstudio", "bin", "lms"),
			filepath.Join(root, ".cache", "lm-studio", "bin", "lms"),
		)
	}
	return paths
}

// CLI runs lms commands.
type CLI struct {
	// Path is the location of the lms executable.
	Path string
	// Timeout bounds a single command; zero means ten minutes.
	Timeout time.Duration
}

// Unload unloads every loaded model.
func (c CLI) Unload(ctx context.Context) error {
	return c.run(ctx, "unload", "-a")
}

// Load loads the model and waits for lms to return. A positive contextLength
// is passed on to the server.
func (c CLI) Load(ctx context.Context, model string, contextLength int) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model must not be empty")
	}
	args := []string{"load", model, "-y"}
	if contextLength > 0 {
		args = append(args, "--context-length", strconv.Itoa(contextLength))
	}
	return c.run(ctx, args...)
}

// run executes lms with the given arguments, discarding its stdout: the
// state of a model is read from the API, not from the CLI output.
func (c CLI) run(ctx context.Context, args ...string) error {
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("%w: path is empty", ErrCLINotFound)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	//nolint:gosec // the executable is located by Locator and the arguments are built here, not by the model
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxCapturedOutput}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxCapturedOutput}

	err := cmd.Run()
	if err == nil {
		return nil
	}
	details := strings.TrimSpace(stderr.String())
	if details == "" {
		details = strings.TrimSpace(stdout.String())
	}
	if len(details) > 500 {
		details = details[len(details)-500:]
	}
	command := "lms " + strings.Join(args, " ")
	if ctx.Err() != nil {
		return fmt.Errorf("%s: timed out after %s: %w", command, timeout, ctx.Err())
	}
	if details == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w: %s", command, err, details)
}

// limitedWriter keeps at most limit bytes and silently drops the rest.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
