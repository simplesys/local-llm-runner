package lmstudio_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/lmstudio"
)

func TestAcquireLockAndRelease(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "config")
	lock, err := lmstudio.AcquireLock(dir, time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	if _, err := os.Stat(lock.Path()); err != nil {
		t.Fatalf("lock file is missing: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(lock.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file still exists after Release(), error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second Release() error = %v, want nil", err)
	}
}

func TestAcquireLockBusy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := lmstudio.AcquireLock(dir, time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	if _, err := lmstudio.AcquireLock(dir, time.Minute); !errors.Is(err, lmstudio.ErrLocked) {
		t.Errorf("AcquireLock() error = %v, want ErrLocked", err)
	}
}

func TestAcquireLockTakesOverStaleFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "old record", content: `{"pid":` + itoa(os.Getpid()) + `,"started_at":"2000-01-01T00:00:00Z"}`},
		{name: "dead process", content: `{"pid":2147483600,"started_at":"` + time.Now().Format(time.RFC3339) + `"}`},
		{name: "malformed content", content: "not json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "lms.lock"), []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write lock file: %v", err)
			}
			lock, err := lmstudio.AcquireLock(dir, time.Minute)
			if err != nil {
				t.Fatalf("AcquireLock() error = %v, want the stale lock to be taken over", err)
			}
			t.Cleanup(func() { _ = lock.Release() })

			data, err := os.ReadFile(lock.Path())
			if err != nil {
				t.Fatalf("read lock file: %v", err)
			}
			var owner struct {
				PID int `json:"pid"`
			}
			if err := json.Unmarshal(data, &owner); err != nil {
				t.Fatalf("decode lock file: %v", err)
			}
			if owner.PID != os.Getpid() {
				t.Errorf("lock owner pid = %d, want %d", owner.PID, os.Getpid())
			}
		})
	}
}

func TestAcquireLockEmptyDirectory(t *testing.T) {
	t.Parallel()

	if _, err := lmstudio.AcquireLock("", time.Minute); err == nil {
		t.Error("AcquireLock(\"\") error = nil, want an error")
	}
}

// itoa avoids pulling strconv into the test for one conversion.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
