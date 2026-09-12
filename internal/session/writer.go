package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Writer appends entries to one session file.
type Writer struct {
	store *Store
	file  *os.File

	mu     sync.Mutex
	meta   Meta
	closed bool
}

// ID returns the session identifier.
func (w *Writer) ID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.meta.ID
}

// Meta returns the metadata accumulated so far.
func (w *Writer) Meta() Meta {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.meta
}

// Append writes one entry; payload is marshaled into the entry data. The file
// is flushed on Close, not on every line: a session that dies mid-write loses
// at most its last entry, and Entries tolerates that.
func (w *Writer) Append(entryType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s entry: %w", entryType, err)
	}
	entry := Entry{At: w.store.now(), Type: entryType, Data: data}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode %s entry: %w", entryType, err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("session writer is closed")
	}
	written, err := w.file.Write(line)
	if err != nil {
		return fmt.Errorf("write session %s: %w", w.meta.ID, err)
	}
	w.meta.Entries++
	w.meta.Bytes += int64(written)
	w.meta.UpdatedAt = entry.At
	return nil
}

// SetTitle records the session title; the first non-empty call wins.
func (w *Writer) SetTitle(text string) error {
	w.mu.Lock()
	if w.meta.Title != "" || text == "" {
		w.mu.Unlock()
		return nil
	}
	w.meta.Title = title(text)
	meta := w.meta
	w.mu.Unlock()

	return w.store.update(meta)
}

// SetModel records a model switch that happened inside the session.
func (w *Writer) SetModel(model string) error {
	w.mu.Lock()
	if model == "" || w.meta.Model == model {
		w.mu.Unlock()
		return nil
	}
	w.meta.Model = model
	meta := w.meta
	w.mu.Unlock()

	return w.store.update(meta)
}

// Close flushes the file and updates the index.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	meta := w.meta
	w.mu.Unlock()

	err := errors.Join(w.file.Sync(), w.file.Close())
	return errors.Join(err, w.store.update(meta))
}
