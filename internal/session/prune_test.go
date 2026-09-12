package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/session"
)

// seed creates count sessions and returns their identifiers, oldest first.
func seed(t *testing.T, store *session.Store, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := range count {
		writer, err := store.Create("/tmp/project", "qwen3")
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := writer.Append(session.TypeNote, map[string]int{"index": i}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		ids = append(ids, writer.ID())
	}
	return ids
}

// rewriteIndex lets a test age sessions without waiting.
func rewriteIndex(t *testing.T, store *session.Store, mutate func(metas []session.Meta) []session.Meta) {
	t.Helper()
	path := filepath.Join(store.Dir(), "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var metas []session.Meta
	if err := json.Unmarshal(data, &metas); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	updated, err := json.MarshalIndent(mutate(metas), "", "  ")
	if err != nil {
		t.Fatalf("encode index: %v", err)
	}
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func TestPruneByCount(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ids := seed(t, store, 5)

	removed, err := store.Prune(session.Policy{MaxSessions: 2})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if removed != 3 {
		t.Errorf("Prune() removed %d sessions, want 3", removed)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("len(metas) = %d, want 2", len(metas))
	}
	if metas[0].ID != ids[4] || metas[1].ID != ids[3] {
		t.Errorf("kept %s and %s, want the two newest (%s, %s)", metas[0].ID, metas[1].ID, ids[4], ids[3])
	}
	for _, id := range ids[:3] {
		if _, err := os.Stat(filepath.Join(store.Dir(), id+".jsonl")); !os.IsNotExist(err) {
			t.Errorf("session %s still exists, want it removed", id)
		}
	}
}

func TestPruneBySize(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	seed(t, store, 4)
	rewriteIndex(t, store, func(metas []session.Meta) []session.Meta {
		for i := range metas {
			metas[i].Bytes = 1000
		}
		return metas
	})

	removed, err := store.Prune(session.Policy{MaxBytes: 2500})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("Prune() removed %d sessions, want 2", removed)
	}
}

func TestPruneByAge(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ids := seed(t, store, 3)
	rewriteIndex(t, store, func(metas []session.Meta) []session.Meta {
		for i := range metas {
			if metas[i].ID == ids[0] {
				metas[i].UpdatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
		}
		return metas
	})

	removed, err := store.Prune(session.Policy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("Prune() removed %d sessions, want 1", removed)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, meta := range metas {
		if meta.ID == ids[0] {
			t.Errorf("session %s survived, want the stale one removed", ids[0])
		}
	}
}

func TestPruneWithoutLimits(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	seed(t, store, 3)

	removed, err := store.Prune(session.Policy{})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if removed != 0 {
		t.Errorf("Prune() removed %d sessions, want 0 when no limit is set", removed)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ids := seed(t, store, 2)
	rewriteIndex(t, store, func(metas []session.Meta) []session.Meta {
		for i := range metas {
			metas[i].UpdatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
		}
		return metas
	})

	if _, err := store.Prune(session.Policy{MaxAge: time.Hour, MaxSessions: 1}); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("len(metas) = %d, want the newest session to survive", len(metas))
	}
	if metas[0].ID != ids[0] && metas[0].ID != ids[1] {
		t.Errorf("kept %s, want one of the seeded sessions", metas[0].ID)
	}
}
