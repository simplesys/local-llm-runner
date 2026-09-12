// Command local-llm-runner is a terminal coding agent for local LLMs served
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

	"github.com/simplesys/local-llm-runner/internal/app"
)

// version is injected at build time: -ldflags "-X main.version=v1.2.3".
var version string

// Exit codes follow the common Unix convention.
const (
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := app.Run(ctx, app.Options{
		Version: resolveVersion(),
		Args:    os.Args[1:],
		Getenv:  os.Getenv,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	})
	stop()

	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "local-llm-runner: %v\n", err)
	if errors.Is(err, app.ErrUsage) {
		fmt.Fprintln(os.Stderr, "Run 'local-llm-runner -h' for usage.")
		os.Exit(exitUsage)
	}
	os.Exit(exitFailure)
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
