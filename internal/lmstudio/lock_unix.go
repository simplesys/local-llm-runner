//go:build !windows

package lmstudio

import (
	"os"
	"syscall"
)

// processAlive reports whether a process with the given PID exists. Signal 0
// performs the permission and existence checks without delivering a signal.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
