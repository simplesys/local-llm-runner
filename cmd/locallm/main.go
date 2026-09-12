// Command locallm is a terminal coding agent for local LLMs served
// through an OpenAI-compatible API (LM Studio by default).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/simplesys/locallm/internal/app"
)

// version is injected at build time: -ldflags "-X main.version=v1.2.3".
var version string

// Exit codes. SIGINT is handled by app, which stops the current turn instead
// of the whole process, so only SIGTERM cancels the root context here.
const (
	exitFailure = 1
	exitUsage   = 2
	exitLimit   = 3
	exitBackend = 4
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	err := app.Run(ctx, app.Options{
		Version:          resolveVersion(),
		Args:             os.Args[1:],
		Getenv:           os.Getenv,
		Stdin:            os.Stdin,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		Color:            isTerminal(os.Stderr) && os.Getenv("NO_COLOR") == "",
		HandleInterrupts: true,
	})
	stop()

	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "locallm: %v\n", err)
	switch {
	case errors.Is(err, app.ErrUsage):
		fmt.Fprintln(os.Stderr, "Run 'locallm -h' for usage.")
		os.Exit(exitUsage)
	case errors.Is(err, app.ErrLimit):
		os.Exit(exitLimit)
	case errors.Is(err, app.ErrBackend):
		os.Exit(exitBackend)
	default:
		os.Exit(exitFailure)
	}
}

// isTerminal reports whether the file is a character device, which is the
// cheapest way to tell a terminal from a pipe or a file.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveVersion prefers the value injected via ldflags and falls back to the
// module version that the go command stamps from VCS information.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
