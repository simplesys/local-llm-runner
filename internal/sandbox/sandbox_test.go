package sandbox_test

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/sandbox"
)

var update = flag.Bool("update", false, "update golden files")

func TestParsePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    sandbox.Policy
		wantErr bool
	}{
		{name: "required", value: "required", want: sandbox.PolicyRequired},
		{name: "best effort", value: "best-effort", want: sandbox.PolicyBestEffort},
		{name: "off", value: "off", want: sandbox.PolicyOff},
		{name: "surrounded by spaces", value: "  off  ", want: sandbox.PolicyOff},
		{name: "nonsense", value: "sometimes", wantErr: true},
		{name: "empty", value: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sandbox.ParsePolicy(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePolicy(%q) error = %v, wantErr = %v", tt.value, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParsePolicy(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestNewPolicyOff(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	box, err := sandbox.New(sandbox.PolicyOff, workspace, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if box.Level() != sandbox.LevelNone {
		t.Errorf("Level() = %q, want %q", box.Level(), sandbox.LevelNone)
	}
	cmd := box.Command(t.Context(), "echo", "ok")
	if filepath.Base(cmd.Path) != "echo" {
		t.Errorf("command = %q, want the plain command", cmd.Path)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "ok" {
		t.Errorf("args = %v, want [echo ok]", cmd.Args)
	}
	if cmd.Dir != workspace {
		t.Errorf("Dir = %q, want %q", cmd.Dir, workspace)
	}
}

func TestNewRequiresAbsolutePaths(t *testing.T) {
	t.Parallel()

	if _, err := sandbox.New(sandbox.PolicyOff, "relative/path", nil); err == nil {
		t.Error("New() error = nil, want an error for a relative workspace")
	}
	if _, err := sandbox.New(sandbox.PolicyOff, t.TempDir(), []string{"cache"}); err == nil {
		t.Error("New() error = nil, want an error for a relative writable path")
	}
}

func TestNewRequiredPolicyWithoutSupport(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "darwin" {
		t.Skip("macOS provides Seatbelt, so the policy can be satisfied")
	}
	_, err := sandbox.New(sandbox.PolicyRequired, t.TempDir(), nil)
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("New() error = %v, want ErrUnavailable", err)
	}
}

func TestBestEffortReportsMissingIsolation(t *testing.T) {
	t.Parallel()

	box, err := sandbox.New(sandbox.PolicyBestEffort, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if box.Level() == sandbox.LevelNone && len(box.Notes()) == 0 {
		t.Error("Notes() is empty while the level is none, want a caveat the user can see")
	}
}

func TestBuildProfileGolden(t *testing.T) {
	t.Parallel()

	box, err := sandbox.New(sandbox.PolicyBestEffort, "/Users/dev/project", []string{"/Users/dev/Library/Caches/go-build", "/Users/dev/go/pkg/mod"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if box.Level() != sandbox.LevelSeatbelt {
		t.Skipf("profiles are only built for Seatbelt, level is %q", box.Level())
	}

	const golden = "testdata/profile.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(box.Profile()), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if box.Profile() != string(want) {
		t.Errorf("Profile() =\n%s\nwant\n%s", box.Profile(), want)
	}
}

func TestBuildProfileEscapesPaths(t *testing.T) {
	t.Parallel()

	box, err := sandbox.New(sandbox.PolicyBestEffort, `/Users/dev/my "odd" project`, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if box.Level() != sandbox.LevelSeatbelt {
		t.Skipf("profiles are only built for Seatbelt, level is %q", box.Level())
	}
	if !strings.Contains(box.Profile(), `(subpath "/Users/dev/my \"odd\" project")`) {
		t.Errorf("Profile() =\n%s\nwant the quotes escaped", box.Profile())
	}
}
