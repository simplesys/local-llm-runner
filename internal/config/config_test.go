package config_test

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/config"
)

// env builds a getenv function from a map.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadPriority(t *testing.T) {
	t.Parallel()

	settings := config.Settings{
		Model:         "from-settings",
		BaseURL:       "http://settings:1234/v1",
		MetricsDir:    "/settings/metrics",
		SandboxPolicy: config.SandboxOff,
		ContextLength: 1111,
		AutoApprove:   true,
	}
	environment := map[string]string{
		config.EnvModel:      "from-env",
		config.EnvBaseURL:    "http://env:1234/v1",
		config.EnvMetricsDir: "/env/metrics",
		config.EnvSandbox:    config.SandboxRequired,
	}

	tests := []struct {
		name       string
		args       []string
		getenv     func(string) string
		settings   config.Settings
		wantModel  string
		wantURL    string
		wantDir    string
		wantPolicy string
	}{
		{
			name: "defaults only", getenv: env(nil),
			wantModel: "", wantURL: config.DefaultBaseURL, wantDir: "", wantPolicy: config.DefaultSandboxPolicy,
		},
		{
			name: "settings override defaults", getenv: env(nil), settings: settings,
			wantModel: "from-settings", wantURL: "http://settings:1234/v1",
			wantDir: "/settings/metrics", wantPolicy: config.SandboxOff,
		},
		{
			name: "environment overrides settings", getenv: env(environment), settings: settings,
			wantModel: "from-env", wantURL: "http://env:1234/v1",
			wantDir: "/env/metrics", wantPolicy: config.SandboxRequired,
		},
		{
			name: "flags override everything",
			args: []string{
				"--model", "from-flag", "--base-url", "http://flag:1234/v1",
				"--metrics-dir", "/flag/metrics", "--sandbox", config.SandboxBestEffort,
			},
			getenv: env(environment), settings: settings,
			wantModel: "from-flag", wantURL: "http://flag:1234/v1",
			wantDir: "/flag/metrics", wantPolicy: config.SandboxBestEffort,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Load(tt.args, tt.getenv, tt.settings)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.wantModel)
			}
			if cfg.BaseURL != tt.wantURL {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.wantURL)
			}
			if cfg.MetricsDir != tt.wantDir {
				t.Errorf("MetricsDir = %q, want %q", cfg.MetricsDir, tt.wantDir)
			}
			if cfg.SandboxPolicy != tt.wantPolicy {
				t.Errorf("SandboxPolicy = %q, want %q", cfg.SandboxPolicy, tt.wantPolicy)
			}
		})
	}
}

func TestLoadSettingsFieldsCarryOver(t *testing.T) {
	t.Parallel()

	settings := config.Settings{ContextLength: 8192, AutoApprove: true}
	cfg, err := config.Load(nil, env(nil), settings)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ContextLength != 8192 || !cfg.AutoApprove {
		t.Errorf("Config = %+v, want context length 8192 and auto approve", cfg)
	}

	cfg, err = config.Load([]string{"--context-length", "4096"}, env(nil), settings)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ContextLength != 4096 {
		t.Errorf("ContextLength = %d, want 4096", cfg.ContextLength)
	}
}

func TestLoadRetentionDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(nil, env(nil), config.Settings{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Retention{
		MaxSessions: config.DefaultMaxSessions,
		MaxBytes:    config.DefaultMaxBytes,
		MaxAgeDays:  config.DefaultMaxAgeDays,
	}
	if cfg.Retention != want {
		t.Errorf("Retention = %+v, want %+v", cfg.Retention, want)
	}

	custom := config.Settings{Sessions: config.Retention{MaxSessions: 5}}
	cfg, err = config.Load(nil, env(nil), custom)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Retention.MaxSessions != 5 || cfg.Retention.MaxBytes != 0 || cfg.Retention.MaxAgeDays != 0 {
		t.Errorf("Retention = %+v, want only the session count limit", cfg.Retention)
	}
}

func TestLoadModeAndFlags(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load([]string{
		"--task", "do it", "--yes", "--no-switch", "--no-session",
		"--max-turns", "5", "--max-tokens", "1000", "--timeout", "90s", "--list-models",
	}, env(nil), config.Settings{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Interactive() {
		t.Error("Interactive() = true, want false when --task is given")
	}
	if !cfg.AutoApprove || !cfg.NoSwitch || !cfg.NoSession || !cfg.ListModels {
		t.Errorf("Config = %+v, want every boolean flag set", cfg)
	}
	if cfg.MaxTurns != 5 || cfg.MaxTokens != 1000 || cfg.Timeout != 90*time.Second {
		t.Errorf("limits = %d/%d/%s, want 5/1000/1m30s", cfg.MaxTurns, cfg.MaxTokens, cfg.Timeout)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--nope"}},
		{name: "positional argument", args: []string{"extra"}},
		{name: "invalid sandbox policy", args: []string{"--sandbox", "wrong"}},
		{name: "negative max turns", args: []string{"--max-turns", "-1"}},
		{name: "negative timeout", args: []string{"--timeout", "-1s"}},
		{name: "unsupported base URL", args: []string{"--base-url", "ftp://host/v1"}},
		{name: "relative base URL", args: []string{"--base-url", "localhost:1234"}},
		{name: "negative context length", args: []string{"--context-length", "-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := config.Load(tt.args, env(nil), config.Settings{}); err == nil {
				t.Errorf("Load(%v) error = nil, want an error", tt.args)
			}
		})
	}
}

func TestLoadHelp(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"-h", "--help"} {
		if _, err := config.Load([]string{arg}, env(nil), config.Settings{}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("Load(%q) error = %v, want flag.ErrHelp", arg, err)
		}
	}
}

func TestLoadEmptyModelIsAllowed(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(nil, env(nil), config.Settings{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model != "" {
		t.Errorf("Model = %q, want it empty so that the runner can pick one", cfg.Model)
	}
}

func TestPrintUsage(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	config.PrintUsage(&out, "/home/dev/.config/locallm/settings.json")
	text := out.String()
	for _, want := range []string{
		"-model", "-base-url", "-task", "-workspace", "-metrics-dir", "-sandbox",
		"-context-length", "-yes", "-no-switch", "-no-session", "-max-turns",
		"-max-tokens", "-timeout", "-list-models", "-version",
		config.EnvModel, config.EnvBaseURL, config.EnvMetricsDir, config.EnvSandbox,
		"/home/dev/.config/locallm/settings.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("PrintUsage() output is missing %q:\n%s", want, text)
		}
	}
}
