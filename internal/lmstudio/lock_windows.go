//go:build windows

package lmstudio

// processAlive cannot be answered cheaply on Windows, so a lock is only ever
// taken over because of its age.
func processAlive(pid int) bool { return pid > 0 }
