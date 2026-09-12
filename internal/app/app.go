// Package app is the composition root: it resolves the configuration and
// wires the LLM client, agent loop and terminal UI together.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/simplesys/local-llm-runner/internal/config"
)

// ErrUsage marks errors caused by invalid command-line usage.
var ErrUsage = errors.New("invalid usage")

const commandName = "local-llm-runner"

// Options carries everything Run needs from the process. Injecting it keeps
// Run free of global state and makes it testable end to end.
type Options struct {
	Version string
	Args    []string            // command-line arguments without the program name
	Getenv  func(string) string // usually os.Getenv
	Stdout  io.Writer
	Stderr  io.Writer
}

// Run executes the application until it completes or ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	cfg, err := config.Load(opts.Args, opts.Getenv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		printUsage(opts.Stdout)
		return nil
	case err != nil:
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}

	if cfg.ShowVersion {
		fmt.Fprintf(opts.Stdout, "%s %s\n", commandName, opts.Version)
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// TODO(agent-loop): start the agent session here once internal/agent
	// and internal/llm are implemented (FR-3, FR-7 in docs/project_description.md).
	mode := "interactive"
	if !cfg.Interactive() {
		mode = "one-shot"
	}
	fmt.Fprintf(opts.Stderr, "model:    %s\nendpoint: %s\nmode:     %s\n\n", cfg.Model, cfg.BaseURL, mode)
	fmt.Fprintln(opts.Stderr, "The agent loop is not implemented yet, see docs/project_description.md.")
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "%s is a terminal coding agent for local LLMs.\n\n", commandName)
	fmt.Fprintf(w, "Usage:\n  %s [flags]\n\n", commandName)
	config.PrintUsage(w)
}
