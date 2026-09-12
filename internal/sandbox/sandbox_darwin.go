//go:build darwin

package sandbox

import (
	"context"
	"os/exec"
)

// seatbeltCommand is the system tool that applies a Seatbelt profile. The API
// is formally deprecated by Apple but remains the only way to sandbox a child
// process without extra dependencies; it is confined to this file.
const seatbeltCommand = "sandbox-exec"

// platformLevel reports the isolation macOS can provide.
func platformLevel() Level {
	if _, err := exec.LookPath(seatbeltCommand); err != nil {
		return LevelNone
	}
	return LevelSeatbelt
}

// command wraps the command into sandbox-exec when a profile is in effect.
func (s Sandbox) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	//nolint:gosec // running a command is the purpose of the package; the caller confirms it
	if s.level != LevelSeatbelt {
		return exec.CommandContext(ctx, name, args...)
	}
	wrapped := append([]string{"-p", s.profile, name}, args...)
	//nolint:gosec // see above: the sandbox wraps the very command it is asked to run
	return exec.CommandContext(ctx, seatbeltCommand, wrapped...)
}
