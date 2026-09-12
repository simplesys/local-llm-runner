package lmstudio_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/lmstudio"
)

// fakeLMS writes an executable stub that records its arguments in argsFile
// and then behaves as the script body tells it to.
func fakeLMS(t *testing.T, body string) (path, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	path = filepath.Join(dir, "lms")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path, argsFile
}

func recordedArgs(t *testing.T, argsFile string) string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded arguments: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func TestLocatorFindUsesPath(t *testing.T) {
	t.Parallel()

	locator := lmstudio.Locator{
		LookPath: func(file string) (string, error) { return "/usr/local/bin/" + file, nil },
		GOOS:     "darwin",
	}
	got, err := locator.Find()
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if got != "/usr/local/bin/lms" {
		t.Errorf("Find() = %q, want %q", got, "/usr/local/bin/lms")
	}
}

func TestLocatorFindCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		goos     string
		home     string
		lmsHome  string
		existing string
		want     string
	}{
		{
			name: "macOS home directory", goos: "darwin", home: "/home/dev",
			existing: "/home/dev/.lmstudio/bin/lms", want: "/home/dev/.lmstudio/bin/lms",
		},
		{
			name: "legacy cache directory", goos: "linux", home: "/home/dev",
			existing: "/home/dev/.cache/lm-studio/bin/lms", want: "/home/dev/.cache/lm-studio/bin/lms",
		},
		{
			name: "windows layout", goos: "windows", home: `C:\Users\dev`,
			existing: filepath.Join(`C:\Users\dev`, ".lmstudio", "bin", "lms.exe"),
			want:     filepath.Join(`C:\Users\dev`, ".lmstudio", "bin", "lms.exe"),
		},
		{
			name: "LMSTUDIO_HOME wins", goos: "linux", home: "/home/dev", lmsHome: "/opt/lm",
			existing: "/opt/lm/.lmstudio/bin/lms", want: "/opt/lm/.lmstudio/bin/lms",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			regular := filepath.Join(t.TempDir(), "lms")
			if err := os.WriteFile(regular, []byte("stub"), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}
			locator := lmstudio.Locator{
				LookPath: func(string) (string, error) { return "", errors.New("not in PATH") },
				HomeDir:  func() (string, error) { return tt.home, nil },
				Getenv: func(key string) string {
					if key == "LMSTUDIO_HOME" {
						return tt.lmsHome
					}
					return ""
				},
				Stat: func(name string) (os.FileInfo, error) {
					if name == tt.existing {
						return os.Stat(regular)
					}
					return nil, os.ErrNotExist
				},
				GOOS: tt.goos,
			}
			got, err := locator.Find()
			if err != nil {
				t.Fatalf("Find() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Find() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocatorFindNotFound(t *testing.T) {
	t.Parallel()

	locator := lmstudio.Locator{
		LookPath: func(string) (string, error) { return "", errors.New("not in PATH") },
		HomeDir:  func() (string, error) { return "/home/dev", nil },
		Getenv:   func(string) string { return "" },
		Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		GOOS:     "linux",
	}
	if _, err := locator.Find(); !errors.Is(err, lmstudio.ErrCLINotFound) {
		t.Errorf("Find() error = %v, want ErrCLINotFound", err)
	}
}

func TestCLIUnload(t *testing.T) {
	t.Parallel()

	path, argsFile := fakeLMS(t, "exit 0\n")
	cli := lmstudio.CLI{Path: path}
	if err := cli.Unload(t.Context()); err != nil {
		t.Fatalf("Unload() error = %v", err)
	}
	if got := recordedArgs(t, argsFile); got != "unload -a" {
		t.Errorf("arguments = %q, want %q", got, "unload -a")
	}
}

func TestCLILoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         string
		contextLength int
		want          string
	}{
		{name: "without context length", model: "qwen3", want: "load qwen3 -y"},
		{name: "with context length", model: "qwen3", contextLength: 8192, want: "load qwen3 -y --context-length 8192"},
		{name: "model name with spaces", model: "my model", want: "load my model -y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path, argsFile := fakeLMS(t, "exit 0\n")
			cli := lmstudio.CLI{Path: path}
			if err := cli.Load(t.Context(), tt.model, tt.contextLength); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := recordedArgs(t, argsFile); got != tt.want {
				t.Errorf("arguments = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCLILoadFailure(t *testing.T) {
	t.Parallel()

	path, _ := fakeLMS(t, "echo 'insufficient system resources' 1>&2\nexit 1\n")
	cli := lmstudio.CLI{Path: path}
	err := cli.Load(t.Context(), "qwen3", 0)
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "insufficient system resources") {
		t.Errorf("Load() error = %v, want it to quote the stderr of lms", err)
	}
}

func TestCLILoadRejectsEmptyModel(t *testing.T) {
	t.Parallel()

	path, argsFile := fakeLMS(t, "exit 0\n")
	cli := lmstudio.CLI{Path: path}
	if err := cli.Load(t.Context(), "  ", 0); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	if _, err := os.Stat(argsFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("lms was executed, want no execution for an empty model name")
	}
}

func TestCLITimeout(t *testing.T) {
	t.Parallel()

	path, _ := fakeLMS(t, "sleep 5\n")
	cli := lmstudio.CLI{Path: path, Timeout: 50 * time.Millisecond}
	start := time.Now()
	err := cli.Unload(t.Context())
	if err == nil {
		t.Fatal("Unload() error = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Unload() took %s, want it to give up quickly", elapsed)
	}
}

func TestCLIWithoutPath(t *testing.T) {
	t.Parallel()

	var cli lmstudio.CLI
	if err := cli.Unload(t.Context()); !errors.Is(err, lmstudio.ErrCLINotFound) {
		t.Errorf("Unload() error = %v, want ErrCLINotFound", err)
	}
}
