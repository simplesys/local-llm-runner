// Package config resolves the runtime configuration from defaults,
// environment variables and command-line flags.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Default values used when neither a flag nor an environment variable is set.
const (
	DefaultModel   = "ornith-1.5-35b-a3b"
	DefaultBaseURL = "http://localhost:1234/v1"
)

// Environment variables that override the defaults.
const (
	EnvModel   = "LOCAL_MODEL"
	EnvBaseURL = "LOCAL_LLM_BASE_URL"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Model is the model identifier as reported by the LLM server.
	Model string
	// BaseURL is the root of the OpenAI-compatible API.
	BaseURL string
	// Task is the prompt for one-shot mode. Empty means interactive mode.
	Task string
	// ShowVersion requests printing the version and exiting.
	ShowVersion bool
}

// Interactive reports whether the session is interactive, i.e. no one-shot
// task was given.
func (c Config) Interactive() bool {
	return c.Task == ""
}

// Load resolves the configuration with priority flags > environment >
// defaults. getenv is usually os.Getenv; it is injected to keep Load free of
// process-wide state.
//
// When help is requested, Load returns an error that matches flag.ErrHelp.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		Model:   envOr(getenv, EnvModel, DefaultModel),
		BaseURL: envOr(getenv, EnvBaseURL, DefaultBaseURL),
	}

	fs := newFlagSet(&cfg)
	fs.SetOutput(io.Discard) // errors are returned to the caller instead
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %q", fs.Args())
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// PrintUsage writes the description of flags and environment variables to w.
func PrintUsage(w io.Writer) {
	cfg := Config{Model: DefaultModel, BaseURL: DefaultBaseURL}
	fs := newFlagSet(&cfg)
	fs.SetOutput(w)

	fmt.Fprintln(w, "Flags:")
	fs.PrintDefaults()
	fmt.Fprintln(w, "\nEnvironment:")
	fmt.Fprintf(w, "  %-20s model identifier (default %q)\n", EnvModel, DefaultModel)
	fmt.Fprintf(w, "  %-20s API base URL (default %q)\n", EnvBaseURL, DefaultBaseURL)
}

func newFlagSet(cfg *Config) *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model identifier as listed by the LLM server")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.Task, "task", "", "run a single task non-interactively and exit")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "print version and exit")
	return fs
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("model must not be empty")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base URL %q: want absolute http or https URL", c.BaseURL)
	}
	return nil
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
