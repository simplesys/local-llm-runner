package metrics_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/metrics"
)

func sampleFile() metrics.File {
	return metrics.File{
		SessionID:  "20260912-100000-abcdef",
		Project:    "/tmp/project",
		Model:      "qwen3-coder-30b",
		BaseURL:    "http://localhost:1234/v1",
		Sandbox:    "seatbelt",
		Mode:       metrics.ModeOneShot,
		Status:     metrics.StatusOK,
		StopReason: "done",
		StartedAt:  time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 9, 12, 10, 0, 12, 0, time.UTC),
		Metrics: metrics.Snapshot{
			ElapsedSeconds: 12.3,
			Turns:          3,
			PromptTokens:   5120,
			TotalTokens:    5990,
		},
	}
}

func TestWriteRoundTrip(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "metrics")
	path, err := metrics.Write(dir, sampleFile())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if want := filepath.Join(dir, "20260912-100000-abcdef.json"); path != want {
		t.Errorf("Write() path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	var got metrics.File
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode metrics file: %v", err)
	}
	if got != sampleFile() {
		t.Errorf("decoded file = %+v, want %+v", got, sampleFile())
	}
	if !strings.Contains(string(data), `"elapsed_seconds": 12.3`) {
		t.Errorf("file =\n%s\nwant elapsed_seconds in seconds", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read metrics directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the metrics file", len(entries))
	}
}

func TestWriteKeepsExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := metrics.Write(dir, sampleFile())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	second, err := metrics.Write(dir, sampleFile())
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if first == second {
		t.Fatalf("second Write() path = %q, want a different file", second)
	}
	if want := filepath.Join(dir, "20260912-100000-abcdef-2.json"); second != want {
		t.Errorf("second Write() path = %q, want %q", second, want)
	}
}

func TestWriteRejectsIncompleteInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		file metrics.File
	}{
		{name: "empty directory", dir: "", file: sampleFile()},
		{name: "empty session id", dir: t.TempDir(), file: metrics.File{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := metrics.Write(tt.dir, tt.file); err == nil {
				t.Error("Write() error = nil, want an error")
			}
		})
	}
}
