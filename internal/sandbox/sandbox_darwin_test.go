//go:build darwin

package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/sandbox"
)

func TestSeatbeltAllowsWorkspaceWrites(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not available")
	}
	workspace := t.TempDir()
	box, err := sandbox.New(sandbox.PolicyRequired, workspace, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if box.Level() != sandbox.LevelSeatbelt {
		t.Fatalf("Level() = %q, want %q", box.Level(), sandbox.LevelSeatbelt)
	}

	cmd := box.Command(t.Context(), "sh", "-c", "echo hi > inside.txt && cat inside.txt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v (%s)", err, output)
	}
	if strings.TrimSpace(string(output)) != "hi" {
		t.Errorf("output = %q, want %q", output, "hi")
	}
	if _, err := os.Stat(filepath.Join(workspace, "inside.txt")); err != nil {
		t.Errorf("workspace file is missing: %v", err)
	}
}

func TestSeatbeltDeniesWritesOutsideWorkspace(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not available")
	}
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	box, err := sandbox.New(sandbox.PolicyRequired, workspace, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cmd := box.Command(t.Context(), "sh", "-c", "echo nope > "+outside)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("command succeeded (%s), want the write to be denied", output)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("the file outside the workspace was created, want it denied")
	}
}
