package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/session"
)

// message mirrors the payload the agent records for a chat message.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// clock returns a deterministic time source that advances one second per
// call.
func clock(start time.Time) func() time.Time {
	var mu sync.Mutex
	current := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		return current
	}
}

func newStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStore(filepath.Join(t.TempDir(), "sessions"), clock(time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func TestCreateAppendEntries(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	writer, err := store.Create("/tmp/project", "qwen3")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := writer.Append(session.TypeMessage, message{Role: "user", Content: "first line\nsecond line"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := writer.SetTitle("first line\nsecond line"); err != nil {
		t.Fatalf("SetTitle() error = %v", err)
	}
	if err := writer.Append(session.TypeMessage, message{Role: "assistant", Content: "hi"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := store.Entries(writer.ID())
	if err != nil {
		t.Fatalf("Entries() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (meta plus two messages)", len(entries))
	}
	if entries[0].Type != session.TypeMeta {
		t.Errorf("entries[0].Type = %q, want %q", entries[0].Type, session.TypeMeta)
	}
	var got message
	if err := json.Unmarshal(entries[1].Data, &got); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if got.Role != "user" {
		t.Errorf("entries[1] = %+v, want the user message first", got)
	}

	metas, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("len(metas) = %d, want 1", len(metas))
	}
	if metas[0].Title != "first line" {
		t.Errorf("Title = %q, want the first line of the user message", metas[0].Title)
	}
	if metas[0].Entries != 3 || metas[0].Bytes == 0 {
		t.Errorf("Meta = %+v, want three entries and a non-zero size", metas[0])
	}
}

func TestEntriesToleratesTruncatedLine(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	writer, err := store.Create("/tmp/project", "qwen3")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := writer.Append(session.TypeMessage, message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	id := writer.ID()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	path := filepath.Join(store.Dir(), id+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open session file: %v", err)
	}
	if _, err := file.WriteString(`{"at":"2026-09-12T10:00:0`); err != nil {
		t.Fatalf("write truncated line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close session file: %v", err)
	}

	entries, err := store.Entries(id)
	if err != nil {
		t.Fatalf("Entries() error = %v, want the truncated line to be skipped", err)
	}
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(entries))
	}
}

func TestListRebuildsIndex(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	for _, model := range []string{"a", "b"} {
		writer, err := store.Create("/tmp/project", model)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := writer.Append(session.TypeMessage, message{Role: "user", Content: "task " + model}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}

	indexPath := filepath.Join(store.Dir(), "index.json")
	tests := []struct {
		name    string
		prepare func(t *testing.T)
	}{
		{name: "index removed", prepare: func(t *testing.T) {
			t.Helper()
			if err := os.Remove(indexPath); err != nil {
				t.Fatalf("remove index: %v", err)
			}
		}},
		{name: "index malformed", prepare: func(t *testing.T) {
			t.Helper()
			if err := os.WriteFile(indexPath, []byte("{broken"), 0o644); err != nil {
				t.Fatalf("write index: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.prepare(t)
			metas, err := store.List()
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(metas) != 2 {
				t.Fatalf("len(metas) = %d, want 2 after the rebuild", len(metas))
			}
			if metas[0].Title == "" {
				t.Error("Title is empty after the rebuild, want it taken from the user message")
			}
			if !metas[0].UpdatedAt.After(metas[1].UpdatedAt) {
				t.Error("List() is not newest first")
			}
		})
	}
}

func TestOpenAppendsToExistingSession(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	writer, err := store.Create("/tmp/project", "qwen3")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := writer.Append(session.TypeMessage, message{Role: "user", Content: "one"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	id := writer.ID()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := store.Open(id)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := reopened.Append(session.TypeMessage, message{Role: "user", Content: "two"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := store.Entries(id)
	if err != nil {
		t.Fatalf("Entries() error = %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("len(entries) = %d, want 3", len(entries))
	}
}

func TestLatest(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	for _, project := range []string{"/tmp/a", "/tmp/b"} {
		writer, err := store.Create(project, "qwen3")
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}

	meta, ok, err := store.Latest("/tmp/a")
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if !ok || meta.Project != "/tmp/a" {
		t.Errorf("Latest(/tmp/a) = %+v, %v, want the session of that project", meta, ok)
	}
	if _, ok, err = store.Latest("/tmp/missing"); err != nil || ok {
		t.Errorf("Latest(/tmp/missing) = %v, %v, want false and no error", ok, err)
	}
}

func TestRejectsMalformedID(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	for _, id := range []string{"../etc/passwd", "bad id", "", "20260912-100000-XYZ"} {
		if _, err := store.Entries(id); err == nil {
			t.Errorf("Entries(%q) error = nil, want an error", id)
		}
		if _, err := store.Open(id); err == nil {
			t.Errorf("Open(%q) error = nil, want an error", id)
		}
	}
}

func TestConcurrentWriters(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			writer, err := store.Create("/tmp/project", "qwen3")
			if err != nil {
				t.Errorf("Create() error = %v", err)
				return
			}
			if err := writer.Append(session.TypeNote, map[string]int{"worker": i}); err != nil {
				t.Errorf("Append() error = %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		})
	}
	wg.Wait()

	metas, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(metas) != 4 {
		t.Errorf("len(metas) = %d, want 4", len(metas))
	}
}

func TestSetTitleKeepsFirstValue(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	writer, err := store.Create("/tmp/project", "qwen3")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := writer.SetTitle("first"); err != nil {
		t.Fatalf("SetTitle() error = %v", err)
	}
	if err := writer.SetTitle("second"); err != nil {
		t.Fatalf("SetTitle() error = %v", err)
	}
	if got := writer.Meta().Title; got != "first" {
		t.Errorf("Title = %q, want %q", got, "first")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSetTitleTruncates(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	writer, err := store.Create("/tmp/project", "qwen3")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	long := strings.Repeat("я", 200)
	if err := writer.SetTitle(long); err != nil {
		t.Fatalf("SetTitle() error = %v", err)
	}
	if got := len([]rune(writer.Meta().Title)); got != 120 {
		t.Errorf("len(Title) = %d runes, want 120", got)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
