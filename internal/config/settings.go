package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Application directory and file names inside the user configuration
// directory.
const (
	appDirName       = "locallm"
	settingsFileName = "settings.json"
	metricsDirName   = "metrics"
	sessionsDirName  = "sessions"
)

// Default session retention: whichever limit is reached first wins.
const (
	DefaultMaxSessions = 50
	DefaultMaxBytes    = 500 << 20
	DefaultMaxAgeDays  = 90
)

// Settings is the persisted part of the configuration. The zero value means
// "nothing configured": every field falls back to a default.
type Settings struct {
	Model         string    `json:"model,omitempty"`
	BaseURL       string    `json:"base_url,omitempty"`
	MetricsDir    string    `json:"metrics_dir,omitempty"`
	SandboxPolicy string    `json:"sandbox_policy,omitempty"`
	ContextLength int       `json:"context_length,omitempty"`
	AutoApprove   bool      `json:"auto_approve,omitempty"`
	Sessions      Retention `json:"sessions"`
}

// Retention limits the session history. A zero limit disables that single
// limit; all three zero at once is rejected unless Disabled is true, because
// an unbounded history would grow without end.
type Retention struct {
	Disabled    bool  `json:"disabled,omitempty"`
	MaxSessions int   `json:"max_sessions,omitempty"`
	MaxBytes    int64 `json:"max_bytes,omitempty"`
	MaxAgeDays  int   `json:"max_age_days,omitempty"`
}

// MaxAge returns the age limit as a duration; zero means no age limit.
func (r Retention) MaxAge() time.Duration {
	return time.Duration(r.MaxAgeDays) * 24 * time.Hour
}

// Validate reports whether the retention limits make sense.
func (r Retention) Validate() error {
	if r.MaxSessions < 0 || r.MaxBytes < 0 || r.MaxAgeDays < 0 {
		return errors.New("session retention limits must not be negative")
	}
	if r.Disabled {
		return nil
	}
	if r.MaxSessions == 0 && r.MaxBytes == 0 && r.MaxAgeDays == 0 {
		return errors.New("session retention: at least one limit must be set, or the history must be disabled")
	}
	return nil
}

// withDefaults fills the limits that were left unset. It is applied to a
// freshly read settings file, where all-zero means "never configured".
func (r Retention) withDefaults() Retention {
	if r.Disabled {
		return r
	}
	if r.MaxSessions == 0 && r.MaxBytes == 0 && r.MaxAgeDays == 0 {
		return Retention{
			MaxSessions: DefaultMaxSessions,
			MaxBytes:    DefaultMaxBytes,
			MaxAgeDays:  DefaultMaxAgeDays,
		}
	}
	return r
}

// Validate reports the first problem with the settings.
func (s Settings) Validate() error {
	if s.ContextLength < 0 {
		return fmt.Errorf("context length must not be negative, got %d", s.ContextLength)
	}
	if s.SandboxPolicy != "" {
		switch s.SandboxPolicy {
		case SandboxRequired, SandboxBestEffort, SandboxOff:
		default:
			return fmt.Errorf("sandbox policy %q: want one of %s, %s, %s",
				s.SandboxPolicy, SandboxRequired, SandboxBestEffort, SandboxOff)
		}
	}
	return s.Sessions.Validate()
}

// EnvConfigDir overrides the configuration directory. It exists so that a
// user can keep settings, sessions and metrics elsewhere, and so that tests
// never touch the real one.
const EnvConfigDir = "LOCAL_LLM_CONFIG_DIR"

// Dir returns the application configuration directory: the override from
// EnvConfigDir, otherwise a directory inside the user configuration
// directory.
func Dir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvConfigDir)); override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(base, appDirName), nil
}

// SettingsPath returns the location of the settings file.
func SettingsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFileName), nil
}

// MetricsDir returns the default directory for metrics files.
func MetricsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, metricsDirName), nil
}

// SessionsDir returns the directory that holds the session history.
func SessionsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionsDirName), nil
}

// LoadSettings reads the settings file. A missing file yields the zero value
// and no error. A malformed file yields the zero value and an error: the
// caller reports it and keeps working with defaults, because bad settings
// must not make the program unusable.
func LoadSettings(path string) (Settings, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the settings path comes from the configuration directory
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings %s: %w", path, err)
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("decode settings %s: %w", path, err)
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	return settings, nil
}

// SaveSettings writes the settings file atomically, creating the directory
// when needed.
func SaveSettings(path string, settings Settings) (err error) {
	if path == "" {
		return errors.New("settings path must not be empty")
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	//nolint:gosec // 0o755 is the project convention for directories (docs/code_style.md)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create settings directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(dir, ".settings-*")
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
