package app_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/simplesys/local-llm-runner/internal/app"
	"github.com/simplesys/local-llm-runner/internal/config"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantStdout string // substring expected in stdout
		wantStderr string // substring expected in stderr
	}{
		{name: "help", args: []string{"-h"}, wantStdout: "Usage:"},
		{name: "version", args: []string{"--version"}, wantStdout: "local-llm-runner v1.2.3\n"},
		{name: "default session", wantStderr: "model:    " + config.DefaultModel},
		{name: "one-shot session", args: []string{"--task", "x"}, wantStderr: "mode:     one-shot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			err := app.Run(t.Context(), app.Options{
				Version: "v1.2.3",
				Args:    tt.args,
				Getenv:  func(string) string { return "" },
				Stdout:  &stdout,
				Stderr:  &stderr,
			})
			if err != nil {
				t.Fatalf("Run(%q) error = %v", tt.args, err)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRunUsageError(t *testing.T) {
	t.Parallel()

	err := app.Run(t.Context(), app.Options{
		Args:   []string{"--unknown"},
		Getenv: func(string) string { return "" },
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if !errors.Is(err, app.ErrUsage) {
		t.Errorf("Run() error = %v, want %v", err, app.ErrUsage)
	}
}
