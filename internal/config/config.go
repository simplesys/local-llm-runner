// Package config resolves the runtime configuration from defaults, the
// settings file, environment variables and command-line flags, in that
// order of increasing priority.
package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Default values used when nothing else provides one.
const (
	DefaultBaseURL       = "http://localhost:1234/v1"
	DefaultSandboxPolicy = "best-effort"
)

// Sandbox policies accepted by the --sandbox flag.
const (
	SandboxRequired   = "required"
	SandboxBestEffort = "best-effort"
	SandboxOff        = "off"
)

// Environment variables that override the settings file.
const (
	EnvModel      = "LOCAL_MODEL"
	EnvBaseURL    = "LOCAL_LLM_BASE_URL"
	EnvMetricsDir = "LOCAL_LLM_METRICS_DIR"
	EnvSandbox    = "LOCAL_LLM_SANDBOX"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Model is the model identifier as reported by the LLM server. Empty
	// means "pick the first loaded model that supports tools".
	Model string
	// BaseURL is the root of the OpenAI-compatible API.
	BaseURL string
	// Task is the prompt for one-shot mode; empty means interactive mode.
	Task string
	// Workspace is the directory the agent works in. Empty means the
	// current directory, which app resolves.
	Workspace string
	// MetricsDir is where per-session metrics files are written. Empty
	// means the default directory next to the settings file.
	MetricsDir string
	// SandboxPolicy is one of required, best-effort or off.
	SandboxPolicy string
	// ContextLength is the context window to load the model with; 0 means
	// the model default.
	ContextLength int
	// AutoApprove skips the confirmation of mutating tool calls.
	AutoApprove bool
	// NoSwitch keeps the runner from loading or unloading models.
	NoSwitch bool
	// NoSession disables session recording.
	NoSession bool
	// MaxTurns, MaxTokens and Timeout cap one task; 0 means no limit.
	MaxTurns  int
	MaxTokens int
	Timeout   time.Duration
	// ListModels prints the model listing and exits.
	ListModels bool
	// ShowVersion prints the version and exits.
	ShowVersion bool
	// Retention limits the session history.
	Retention Retention
}

// Interactive reports whether the session is interactive, that is no
// one-shot task was given.
func (c Config) Interactive() bool { return c.Task == "" }

// Load resolves the configuration with priority flags > environment >
// settings file > defaults. getenv is usually os.Getenv; it is injected to
// keep Load free of process-wide state.
//
// When help is requested, Load returns an error that matches flag.ErrHelp.
func Load(args []string, getenv func(string) string, settings Settings) (Config, error) {
	cfg := Config{
		Model:         firstNonEmpty(getenv(EnvModel), settings.Model),
		BaseURL:       firstNonEmpty(getenv(EnvBaseURL), settings.BaseURL, DefaultBaseURL),
		MetricsDir:    firstNonEmpty(getenv(EnvMetricsDir), settings.MetricsDir),
		SandboxPolicy: firstNonEmpty(getenv(EnvSandbox), settings.SandboxPolicy, DefaultSandboxPolicy),
		ContextLength: settings.ContextLength,
		AutoApprove:   settings.AutoApprove,
		Retention:     settings.Sessions.withDefaults(),
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
func PrintUsage(w io.Writer, settingsPath string) {
	cfg := Config{BaseURL: DefaultBaseURL, SandboxPolicy: DefaultSandboxPolicy}
	fs := newFlagSet(&cfg)
	fs.SetOutput(w)

	fmt.Fprintln(w, "Flags:")
	fs.PrintDefaults()
	fmt.Fprintln(w, "\nEnvironment:")
	fmt.Fprintf(w, "  %-24s model identifier\n", EnvModel)
	fmt.Fprintf(w, "  %-24s API base URL (default %q)\n", EnvBaseURL, DefaultBaseURL)
	fmt.Fprintf(w, "  %-24s directory for per-session metrics files\n", EnvMetricsDir)
	fmt.Fprintf(w, "  %-24s sandbox policy (default %q)\n", EnvSandbox, DefaultSandboxPolicy)
	fmt.Fprintf(w, "  %-24s directory for settings, sessions and metrics\n", EnvConfigDir)
	if settingsPath != "" {
		fmt.Fprintf(w, "\nSettings file: %s\n", settingsPath)
	}
}

func newFlagSet(cfg *Config) *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model identifier as listed by the LLM server")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.Task, "task", "", "run a single task non-interactively and exit")
	fs.StringVar(&cfg.Workspace, "workspace", "", "directory the agent works in (default: current directory)")
	fs.StringVar(&cfg.MetricsDir, "metrics-dir", cfg.MetricsDir, "directory for per-session metrics files")
	fs.StringVar(&cfg.SandboxPolicy, "sandbox", cfg.SandboxPolicy, "sandbox policy: required, best-effort or off")
	fs.IntVar(&cfg.ContextLength, "context-length", cfg.ContextLength, "context window to load the model with")
	fs.BoolVar(&cfg.AutoApprove, "yes", cfg.AutoApprove, "approve file changes and commands without asking")
	fs.BoolVar(&cfg.NoSwitch, "no-switch", false, "do not load or unload models")
	fs.BoolVar(&cfg.NoSession, "no-session", false, "do not record the session")
	fs.IntVar(&cfg.MaxTurns, "max-turns", 0, "stop after this many model requests (0: no limit)")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", 0, "stop after this many tokens (0: no limit)")
	fs.DurationVar(&cfg.Timeout, "timeout", 0, "stop after this much time (0: no limit)")
	fs.BoolVar(&cfg.ListModels, "list-models", false, "print the models of the server and exit")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "print version and exit")
	return fs
}

func (c Config) validate() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("base URL %q: want absolute http or https URL", c.BaseURL)
	}
	switch c.SandboxPolicy {
	case SandboxRequired, SandboxBestEffort, SandboxOff:
	default:
		return fmt.Errorf("sandbox policy %q: want one of %s, %s, %s",
			c.SandboxPolicy, SandboxRequired, SandboxBestEffort, SandboxOff)
	}
	for _, limit := range []struct {
		name  string
		value int
	}{
		{name: "context length", value: c.ContextLength},
		{name: "max turns", value: c.MaxTurns},
		{name: "max tokens", value: c.MaxTokens},
	} {
		if limit.value < 0 {
			return fmt.Errorf("%s must not be negative, got %d", limit.name, limit.value)
		}
	}
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative, got %s", c.Timeout)
	}
	if err := c.Retention.Validate(); err != nil {
		return err
	}
	return nil
}

// firstNonEmpty returns the first value that is not empty after trimming.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
