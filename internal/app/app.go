// Package app is the composition root: it resolves the configuration and
// wires the LM Studio client, the agent loop, the tools and the terminal
// interface together.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/simplesys/locallm/internal/config"
	"github.com/simplesys/locallm/internal/ui"
)

// Errors that the process turns into distinguishable exit codes.
var (
	// ErrUsage marks errors caused by invalid command-line usage.
	ErrUsage = errors.New("invalid usage")
	// ErrLimit reports that a budget limit stopped the work.
	ErrLimit = errors.New("limit reached")
	// ErrBackend reports that the LLM server is unavailable or cannot serve
	// the requested model.
	ErrBackend = errors.New("llm backend unavailable")
)

const commandName = "locallm"

// HTTP transport timeouts. The client itself must not have a Timeout: it
// would abort a long generation.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 60 * time.Second
)

// Options carries everything Run needs from the process. Injecting it keeps
// Run free of global state and makes it testable end to end.
type Options struct {
	Version string
	Args    []string            // command-line arguments without the program name
	Getenv  func(string) string // usually os.Getenv
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Color enables ANSI escapes; the process decides by looking at the
	// terminal.
	Color bool
	// HandleInterrupts lets the interactive loop react to SIGINT itself:
	// the first press stops the current turn, the second quits. Tests
	// leave it off.
	HandleInterrupts bool
}

// Run executes the application until it completes or ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	console := &ui.Console{In: opts.Stdin, Out: opts.Stdout, Err: opts.Stderr, Color: opts.Color}

	settingsPath, pathErr := config.SettingsPath()
	settings := config.Settings{}
	if pathErr != nil {
		console.Warn("settings are unavailable: %v", pathErr)
	} else {
		loaded, err := config.LoadSettings(settingsPath)
		if err != nil {
			console.Warn("%v; using defaults", err)
		}
		settings = loaded
	}

	cfg, err := config.Load(opts.Args, opts.Getenv, settings)
	switch {
	case errors.Is(err, flag.ErrHelp):
		printUsage(opts.Stdout, settingsPath)
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

	cfg, err = resolvePaths(cfg)
	if err != nil {
		return err
	}
	return runSession(ctx, opts, cfg, console)
}

// resolvePaths fills in the directories that depend on the process: the
// workspace and the metrics directory.
func resolvePaths(cfg config.Config) (config.Config, error) {
	workspace := cfg.Workspace
	if workspace == "" {
		current, err := os.Getwd()
		if err != nil {
			return cfg, fmt.Errorf("determine working directory: %w", err)
		}
		workspace = current
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return cfg, fmt.Errorf("resolve workspace %s: %w", workspace, err)
	}
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		return cfg, fmt.Errorf("%w: workspace %s is not a directory", ErrUsage, absolute)
	}
	cfg.Workspace = absolute

	if cfg.MetricsDir == "" {
		dir, err := config.MetricsDir()
		if err == nil {
			cfg.MetricsDir = dir
		}
	}
	return cfg, nil
}

// newHTTPClient builds the client used for every request to the LLM server.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			ResponseHeaderTimeout: responseHeaderTimeout,
			MaxIdleConns:          4,
		},
	}
}

func printUsage(w io.Writer, settingsPath string) {
	fmt.Fprintf(w, "%s is a terminal coding agent for local LLMs.\n\n", commandName)
	fmt.Fprintf(w, "Usage:\n  %s [flags]\n  %s --task \"...\"\n\n", commandName, commandName)
	config.PrintUsage(w, settingsPath)
	fmt.Fprintf(w, "\nExit codes: 0 success, 1 failure, 2 usage, 3 limit reached, 4 backend unavailable.\n")
}
