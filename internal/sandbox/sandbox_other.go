//go:build !darwin

package sandbox

import (
	"context"
	"os/exec"
)

// platformLevel reports that no isolation is available. Linux support is
// added separately.
func platformLevel() Level { return LevelNone }

// command runs the command as is.
func (s Sandbox) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	//nolint:gosec // running a command is the purpose of the package; the caller confirms it
	return exec.CommandContext(ctx, name, args...)
}
