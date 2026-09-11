package config_test

import (
	"errors"
	"flag"
	"testing"

	"github.com/simplesys/local-llm-runner/internal/config"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	defaults := config.Config{Model: config.DefaultModel, BaseURL: config.DefaultBaseURL}

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want config.Config
	}{
		{
			name: "defaults",
			want: defaults,
		},
		{
			name: "environment overrides defaults",
			env: map[string]string{
				config.EnvModel:   "qwen2.5-coder",
				config.EnvBaseURL: "http://127.0.0.1:8080/v1",
			},
			want: config.Config{Model: "qwen2.5-coder", BaseURL: "http://127.0.0.1:8080/v1"},
		},
		{
			name: "flags override environment",
			args: []string{"--model", "llama", "--base-url=https://llm.local/v1"},
			env:  map[string]string{config.EnvModel: "qwen2.5-coder"},
			want: config.Config{Model: "llama", BaseURL: "https://llm.local/v1"},
		},
		{
			name: "task enables one-shot mode",
			args: []string{"--task", "fix the tests"},
			want: config.Config{Model: config.DefaultModel, BaseURL: config.DefaultBaseURL, Task: "fix the tests"},
		},
		{
			name: "flag order does not matter",
			args: []string{"--task", "x", "--model", "m"},
			want: config.Config{Model: "m", BaseURL: config.DefaultBaseURL, Task: "x"},
		},
		{
			name: "version",
			args: []string{"-version"},
			want: config.Config{Model: config.DefaultModel, BaseURL: config.DefaultBaseURL, ShowVersion: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := config.Load(tt.args, fakeEnv(tt.env))
			if err != nil {
				t.Fatalf("Load(%q) error = %v", tt.args, err)
			}
			if got != tt.want {
				t.Errorf("Load(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantHelp bool
	}{
		{name: "help", args: []string{"-h"}, wantHelp: true},
		{name: "unknown flag", args: []string{"--nope"}},
		{name: "positional argument", args: []string{"extra"}},
		{name: "empty model", args: []string{"--model", " "}},
		{name: "base URL without scheme", args: []string{"--base-url", "localhost:1234"}},
		{name: "unsupported scheme", args: []string{"--base-url", "ftp://localhost/v1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(tt.args, fakeEnv(nil))
			if err == nil {
				t.Fatalf("Load(%q) error = nil, want error", tt.args)
			}
			if got := errors.Is(err, flag.ErrHelp); got != tt.wantHelp {
				t.Errorf("errors.Is(%v, flag.ErrHelp) = %t, want %t", err, got, tt.wantHelp)
			}
		})
	}
}

func TestConfigInteractive(t *testing.T) {
	t.Parallel()

	if !(config.Config{}).Interactive() {
		t.Error("Interactive() = false for empty task, want true")
	}
	if (config.Config{Task: "x"}).Interactive() {
		t.Error("Interactive() = true for non-empty task, want false")
	}
}

func fakeEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}
