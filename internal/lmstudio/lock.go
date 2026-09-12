package lmstudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked reports that another instance holds the model lock.
var ErrLocked = errors.New("model lock held by another process")

// defaultStaleAfter is how long a lock file may stay untouched before it is
// considered abandoned.
const defaultStaleAfter = 10 * time.Minute

// lockFileName is the name of the lock file inside the configuration
// directory.
const lockFileName = "lms.lock"

// Lock is an advisory single-writer lock that keeps two instances from
// switching models under each other.
type Lock struct {
	path     string
	released bool
}

// lockOwner is the content of the lock file.
type lockOwner struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// AcquireLock takes the model lock in dir, creating the directory when
// needed. A lock whose owner is gone, or which is older than staleAfter, is
// taken over. staleAfter <= 0 means ten minutes.
func AcquireLock(dir string, staleAfter time.Duration) (*Lock, error) {
	if dir == "" {
		return nil, errors.New("lock directory must not be empty")
	}
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}
	//nolint:gosec // 0o755 is the project convention for directories (docs/code_style.md)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, lockFileName)

	for attempt := range 2 {
		err := writeLockFile(path)
		if err == nil {
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create lock %s: %w", path, err)
		}
		if attempt == 1 || !lockIsStale(path, staleAfter) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale lock %s: %w", path, err)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrLocked, path)
}

// writeLockFile creates the lock file exclusively and records the owner.
func writeLockFile(path string) (err error) {
	//nolint:gosec // 0o644 is the project convention for files (docs/code_style.md)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()

	owner := lockOwner{PID: os.Getpid(), StartedAt: time.Now()}
	return json.NewEncoder(file).Encode(owner)
}

// lockIsStale reports whether the existing lock file can be taken over.
// Unreadable or malformed content counts as stale: the file is then no use to
// anybody.
func lockIsStale(path string, staleAfter time.Duration) bool {
	data, err := os.ReadFile(path) //nolint:gosec // the path is built from the configuration directory
	if err != nil {
		return true
	}
	var owner lockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return true
	}
	if time.Since(owner.StartedAt) > staleAfter {
		return true
	}
	if owner.PID == os.Getpid() {
		return false
	}
	return !processAlive(owner.PID)
}

// Release removes the lock file. Calling it more than once is safe.
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove lock %s: %w", l.path, err)
	}
	return nil
}

// Path returns the location of the lock file.
func (l *Lock) Path() string { return l.path }
