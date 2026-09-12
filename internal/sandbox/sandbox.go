// Package sandbox prepares commands for execution under the isolation the
// host can provide: Seatbelt on macOS, none elsewhere for now. The policy
// says what to do when isolation is unavailable, and the level reached is
// always reported, never assumed.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Level is the isolation actually achieved.
type Level string

// Isolation levels.
const (
	LevelNone     Level = "none"
	LevelSeatbelt Level = "seatbelt"
	LevelLandlock Level = "landlock"
)

// Policy says what to do when the platform cannot provide isolation.
type Policy string

// Isolation policies.
const (
	PolicyRequired   Policy = "required"
	PolicyBestEffort Policy = "best-effort"
	PolicyOff        Policy = "off"
)

// ErrUnavailable reports that the requested policy cannot be satisfied on
// this platform.
var ErrUnavailable = errors.New("sandbox unavailable")

// ParsePolicy converts a configuration string into a Policy.
func ParsePolicy(value string) (Policy, error) {
	switch Policy(strings.TrimSpace(value)) {
	case PolicyRequired:
		return PolicyRequired, nil
	case PolicyBestEffort:
		return PolicyBestEffort, nil
	case PolicyOff:
		return PolicyOff, nil
	default:
		return "", fmt.Errorf("sandbox policy %q: want one of %s, %s, %s",
			value, PolicyRequired, PolicyBestEffort, PolicyOff)
	}
}

// Sandbox prepares commands for execution under isolation.
type Sandbox struct {
	policy    Policy
	level     Level
	workspace string
	profile   string
	notes     []string
}

// New builds a sandbox for the workspace directory. Writable lists extra
// absolute paths the command may write to, such as build and module caches.
// With PolicyRequired and no platform support New returns ErrUnavailable.
func New(policy Policy, workspace string, writable []string) (Sandbox, error) {
	if !filepath.IsAbs(workspace) {
		return Sandbox{}, fmt.Errorf("workspace %q: want an absolute path", workspace)
	}
	for _, path := range writable {
		if !filepath.IsAbs(path) {
			return Sandbox{}, fmt.Errorf("writable path %q: want an absolute path", path)
		}
	}

	sandbox := Sandbox{policy: policy, workspace: filepath.Clean(workspace), level: LevelNone}
	if policy != PolicyOff {
		sandbox.level = platformLevel()
	}
	if policy == PolicyRequired && sandbox.level == LevelNone {
		return Sandbox{}, fmt.Errorf("%w: policy %s cannot be satisfied here", ErrUnavailable, policy)
	}

	switch sandbox.level {
	case LevelSeatbelt:
		sandbox.profile = buildProfile(resolvePath(sandbox.workspace), resolvePaths(writable))
	case LevelLandlock:
		// The Linux implementation is added separately; see the task list.
	case LevelNone:
		if policy == PolicyBestEffort {
			sandbox.notes = append(sandbox.notes, "commands run without sandbox isolation on this platform")
		}
	}
	return sandbox, nil
}

// Level reports the isolation this sandbox provides.
func (s Sandbox) Level() Level { return s.level }

// Policy reports the configured policy.
func (s Sandbox) Policy() Policy { return s.policy }

// Notes returns the caveats of the isolation in effect, for the interface and
// the result file.
func (s Sandbox) Notes() []string { return slices.Clone(s.notes) }

// Command builds the command to execute, wrapped into the sandbox when one is
// available. The working directory is always the workspace.
func (s Sandbox) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := s.command(ctx, name, args...)
	cmd.Dir = s.workspace
	return cmd
}

// Profile returns the Seatbelt profile in effect, empty on other platforms.
// It exists for diagnostics.
func (s Sandbox) Profile() string { return s.profile }

// resolvePath expands symbolic links, because Seatbelt matches real paths:
// on macOS a temporary directory under /var actually lives in /private/var.
// A path that cannot be resolved (it may not exist yet) is kept as is.
func resolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// resolvePaths applies resolvePath to every element.
func resolvePaths(paths []string) []string {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		resolved = append(resolved, resolvePath(path))
	}
	return resolved
}

// buildProfile renders the Seatbelt profile: reading is allowed everywhere
// because compilers need system libraries, writing only inside the workspace
// and the named caches, and the network is denied outright.
func buildProfile(workspace string, writable []string) string {
	paths := append([]string{workspace}, writable...)
	slices.Sort(paths)
	paths = slices.Compact(paths)

	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n(allow file-read*)\n")
	profile.WriteString("(allow process-exec)\n(allow process-fork)\n(allow sysctl-read)\n")
	profile.WriteString("(allow signal (target self))\n(deny network*)\n")
	profile.WriteString("(allow file-write* (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/dtracehelper\"))\n")
	profile.WriteString("(allow file-write*\n")
	for _, path := range paths {
		fmt.Fprintf(&profile, "  (subpath %s)\n", quoteProfilePath(path))
	}
	profile.WriteString(")\n")
	return profile.String()
}

// quoteProfilePath renders a path as a Seatbelt string literal.
func quoteProfilePath(path string) string {
	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
