package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/simplesys/locallm/internal/config"
)

func TestLoadSettingsMissingFile(t *testing.T) {
	t.Parallel()

	got, err := config.LoadSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil for a missing file", err)
	}
	if got != (config.Settings{}) {
		t.Errorf("LoadSettings() = %+v, want the zero value", got)
	}
}

func TestLoadSettingsMalformedFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "not json", content: "{"},
		{name: "invalid policy", content: `{"sandbox_policy":"wrong"}`},
		{name: "negative context length", content: `{"context_length":-1}`},
		{name: "retention without limits", content: `{"sessions":{"max_sessions":0,"max_bytes":0,"max_age_days":0,"disabled":false}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write settings: %v", err)
			}
			got, err := config.LoadSettings(path)
			if err == nil {
				t.Error("LoadSettings() error = nil, want an error")
			}
			if got != (config.Settings{}) {
				t.Errorf("LoadSettings() = %+v, want the zero value so that defaults apply", got)
			}
		})
	}
}

func TestSaveAndLoadSettings(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	want := config.Settings{
		Model:         "qwen3-coder-30b",
		BaseURL:       "http://localhost:1234/v1",
		MetricsDir:    "/tmp/metrics",
		SandboxPolicy: config.SandboxRequired,
		ContextLength: 32768,
		AutoApprove:   true,
		Sessions:      config.Retention{MaxSessions: 20, MaxBytes: 1 << 20, MaxAgeDays: 30},
	}
	if err := config.SaveSettings(path, want); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	got, err := config.LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got != want {
		t.Errorf("LoadSettings() = %+v, want %+v", got, want)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the settings file", len(entries))
	}

	want.Model = "another-model"
	if err := config.SaveSettings(path, want); err != nil {
		t.Fatalf("second SaveSettings() error = %v", err)
	}
	got, err = config.LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got.Model != "another-model" {
		t.Errorf("Model = %q, want the overwritten value", got.Model)
	}
}

func TestSaveSettingsRejectsInvalid(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	invalid := config.Settings{Sessions: config.Retention{}}
	if err := config.SaveSettings(path, invalid); err == nil {
		t.Error("SaveSettings() error = nil, want an error for a history without limits")
	}
	if err := config.SaveSettings("", config.Settings{Sessions: config.Retention{MaxSessions: 1}}); err == nil {
		t.Error("SaveSettings(\"\") error = nil, want an error")
	}
}

func TestRetentionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retention config.Retention
		wantErr   bool
	}{
		{name: "all limits set", retention: config.Retention{MaxSessions: 50, MaxBytes: 1, MaxAgeDays: 90}},
		{name: "one limit set", retention: config.Retention{MaxSessions: 10}},
		{name: "history disabled", retention: config.Retention{Disabled: true}},
		{name: "no limits", retention: config.Retention{}, wantErr: true},
		{name: "negative limit", retention: config.Retention{MaxSessions: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.retention.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDirectoryHelpers(t *testing.T) {
	t.Parallel()

	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	settings, err := config.SettingsPath()
	if err != nil {
		t.Fatalf("SettingsPath() error = %v", err)
	}
	if got := filepath.Dir(settings); got != dir {
		t.Errorf("SettingsPath() lives in %q, want %q", got, dir)
	}
	for name, get := range map[string]func() (string, error){
		"MetricsDir":  config.MetricsDir,
		"SessionsDir": config.SessionsDir,
	} {
		path, err := get()
		if err != nil {
			t.Fatalf("%s() error = %v", name, err)
		}
		if filepath.Dir(path) != dir {
			t.Errorf("%s() = %q, want it inside %q", name, path, dir)
		}
	}
}
