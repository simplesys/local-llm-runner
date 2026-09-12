package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Session statuses recorded in the metrics file.
const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusLimit    = "limit"
	StatusCanceled = "canceled"
)

// Session modes recorded in the metrics file.
const (
	ModeInteractive = "interactive"
	ModeOneShot     = "one-shot"
)

// File is the per-session metrics file: the artifact that makes runs on
// different models comparable.
type File struct {
	SessionID  string    `json:"session_id"`
	Project    string    `json:"project"`
	Model      string    `json:"model"`
	BaseURL    string    `json:"base_url"`
	Sandbox    string    `json:"sandbox"`
	Mode       string    `json:"mode"`
	Status     string    `json:"status"`
	StopReason string    `json:"stop_reason,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Metrics    Snapshot  `json:"metrics"`
}

// Write saves the file as <dir>/<session id>.json, creating dir when needed.
// The write is atomic: a temporary file plus os.Rename. An existing file is
// never overwritten; a numeric suffix is added instead.
func Write(dir string, file File) (string, error) {
	if dir == "" {
		return "", errors.New("metrics directory must not be empty")
	}
	if file.SessionID == "" {
		return "", errors.New("session id must not be empty")
	}
	//nolint:gosec // 0o755 is the project convention for directories (docs/code_style.md)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create metrics directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode metrics: %w", err)
	}
	data = append(data, '\n')

	path := freePath(dir, file.SessionID)
	if err := writeAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// freePath returns a path that no file occupies yet.
func freePath(dir, sessionID string) string {
	path := filepath.Join(dir, sessionID+".json")
	for suffix := 2; ; suffix++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = filepath.Join(dir, sessionID+"-"+strconv.Itoa(suffix)+".json")
	}
}

// writeAtomic writes data through a temporary file in the same directory.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".metrics-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempName)
		}
	}()

	if _, err = temp.Write(data); err != nil {
		err = errors.Join(fmt.Errorf("write %s: %w", tempName, err), temp.Close())
		return err
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tempName, err)
	}
	//nolint:gosec // 0o644 is the project convention for files (docs/code_style.md)
	if err = os.Chmod(tempName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tempName, err)
	}
	if err = os.Rename(tempName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tempName, path, err)
	}
	return nil
}
