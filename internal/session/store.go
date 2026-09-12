package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// File names inside the session directory.
const (
	indexFileName = "index.json"
	sessionSuffix = ".jsonl"
)

// Store keeps session files and their index in one directory.
type Store struct {
	dir string
	now func() time.Time

	mu sync.Mutex
}

// NewStore opens the session directory, creating it when needed. now is
// injected for tests; nil means time.Now.
func NewStore(dir string, now func() time.Time) (*Store, error) {
	if dir == "" {
		return nil, errors.New("session directory must not be empty")
	}
	if now == nil {
		now = time.Now
	}
	//nolint:gosec // 0o755 is the project convention for directories (docs/code_style.md)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session directory %s: %w", dir, err)
	}
	return &Store{dir: dir, now: now}, nil
}

// Dir returns the directory the store works in.
func (s *Store) Dir() string { return s.dir }

// Create starts a new session and returns a writer for it.
func (s *Store) Create(project, model string) (*Writer, error) {
	startedAt := s.now()
	id, err := newID(startedAt)
	if err != nil {
		return nil, err
	}
	meta := Meta{
		ID:        id,
		Project:   project,
		Model:     model,
		StartedAt: startedAt,
		UpdatedAt: startedAt,
	}
	writer, err := s.openWriter(meta)
	if err != nil {
		return nil, err
	}
	if err := writer.Append(TypeMeta, meta); err != nil {
		return nil, errors.Join(err, writer.Close())
	}
	return writer, nil
}

// Open appends to an existing session.
func (s *Store) Open(id string) (*Writer, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, meta := range metas {
		if meta.ID == id {
			return s.openWriter(meta)
		}
	}
	return nil, fmt.Errorf("session %s not found in %s", id, s.dir)
}

// openWriter opens the session file for appending.
func (s *Store) openWriter(meta Meta) (*Writer, error) {
	path := s.sessionPath(meta.ID)
	//nolint:gosec // 0o644 is the project convention for files (docs/code_style.md)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", path, err)
	}
	return &Writer{store: s, file: file, meta: meta}, nil
}

// sessionPath returns the file that holds the session.
func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.dir, id+sessionSuffix)
}

// List returns session metadata, newest first. A missing or malformed index
// is rebuilt by scanning the directory.
func (s *Store) List() ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

func (s *Store) list() ([]Meta, error) {
	metas, err := s.readIndex()
	if err != nil {
		metas, err = s.rebuildIndex()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	return metas, nil
}

// readIndex loads the index file.
func (s *Store) readIndex() ([]Meta, error) {
	path := filepath.Join(s.dir, indexFileName)
	data, err := os.ReadFile(path) //nolint:gosec // the index lives in the session directory of this store
	if err != nil {
		return nil, fmt.Errorf("read index %s: %w", path, err)
	}
	var metas []Meta
	if err := json.Unmarshal(data, &metas); err != nil {
		return nil, fmt.Errorf("decode index %s: %w", path, err)
	}
	return metas, nil
}

// rebuildIndex scans the directory and writes a fresh index.
func (s *Store) rebuildIndex() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read session directory %s: %w", s.dir, err)
	}
	var metas []Meta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionSuffix) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), sessionSuffix)
		if validateID(id) != nil {
			continue
		}
		meta, err := s.scanSession(id)
		if err != nil {
			continue // an unreadable file must not hide the rest of the history
		}
		metas = append(metas, meta)
	}
	if err := s.writeIndex(metas); err != nil {
		return nil, err
	}
	return metas, nil
}

// scanSession reconstructs the metadata of one session from its file.
func (s *Store) scanSession(id string) (Meta, error) {
	entries, err := s.entries(id)
	if err != nil {
		return Meta{}, err
	}
	info, err := os.Stat(s.sessionPath(id))
	if err != nil {
		return Meta{}, fmt.Errorf("stat session %s: %w", id, err)
	}

	meta := Meta{ID: id, Entries: len(entries), Bytes: info.Size(), UpdatedAt: info.ModTime()}
	for _, entry := range entries {
		switch entry.Type {
		case TypeMeta:
			var stored Meta
			if json.Unmarshal(entry.Data, &stored) == nil {
				meta.Project = stored.Project
				meta.Model = stored.Model
				meta.StartedAt = stored.StartedAt
				if stored.Title != "" {
					meta.Title = stored.Title
				}
			}
		case TypeMessage:
			if meta.Title != "" {
				continue
			}
			var message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if json.Unmarshal(entry.Data, &message) == nil && message.Role == "user" {
				meta.Title = title(message.Content)
			}
		}
	}
	if meta.StartedAt.IsZero() && len(entries) > 0 {
		meta.StartedAt = entries[0].At
	}
	return meta, nil
}

// writeIndex stores the index atomically.
func (s *Store) writeIndex(metas []Meta) (err error) {
	if metas == nil {
		metas = []Meta{}
	}
	data, err := json.MarshalIndent(metas, "", "  ")
	if err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(s.dir, ".index-*")
	if err != nil {
		return fmt.Errorf("create temporary index in %s: %w", s.dir, err)
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
	target := filepath.Join(s.dir, indexFileName)
	if err = os.Rename(tempName, target); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tempName, target, err)
	}
	return nil
}

// update stores the metadata of one session in the index.
func (s *Store) update(meta Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	metas, err := s.list()
	if err != nil {
		return err
	}
	replaced := false
	for i := range metas {
		if metas[i].ID == meta.ID {
			metas[i] = meta
			replaced = true
			break
		}
	}
	if !replaced {
		metas = append(metas, meta)
	}
	return s.writeIndex(metas)
}

// Entries reads every well-formed entry of a session. A truncated last line
// is skipped rather than reported: a session may be cut short by a crash.
func (s *Store) Entries(id string) ([]Entry, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.entries(id)
}

func (s *Store) entries(id string) ([]Entry, error) {
	path := s.sessionPath(id)
	data, err := os.ReadFile(path) //nolint:gosec // the identifier is validated against idPattern before use
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", path, err)
	}
	var entries []Entry
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Latest returns the newest session of the project, reporting false when
// there is none.
func (s *Store) Latest(project string) (Meta, bool, error) {
	metas, err := s.List()
	if err != nil {
		return Meta{}, false, err
	}
	for _, meta := range metas {
		if meta.Project == project {
			return meta, true, nil
		}
	}
	return Meta{}, false, nil
}

// Prune deletes sessions that exceed the policy, oldest first, and returns
// how many were removed. The newest session is always kept: it is the one the
// user is most likely working with.
func (s *Store) Prune(policy Policy) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metas, err := s.list()
	if err != nil {
		return 0, err
	}
	if len(metas) <= 1 {
		return 0, nil
	}

	// metas is newest first; walk from the oldest end.
	keep := make([]Meta, 0, len(metas))
	var doomed []Meta
	now := s.now()

	if policy.MaxAge > 0 {
		for _, meta := range metas {
			if now.Sub(meta.UpdatedAt) > policy.MaxAge {
				doomed = append(doomed, meta)
				continue
			}
			keep = append(keep, meta)
		}
	} else {
		keep = append(keep, metas...)
	}

	if policy.MaxSessions > 0 && len(keep) > policy.MaxSessions {
		doomed = append(doomed, keep[policy.MaxSessions:]...)
		keep = keep[:policy.MaxSessions]
	}

	if policy.MaxBytes > 0 {
		var total int64
		for i, meta := range keep {
			total += meta.Bytes
			if total > policy.MaxBytes && i > 0 {
				doomed = append(doomed, keep[i:]...)
				keep = keep[:i]
				break
			}
		}
	}

	// Never delete the newest session, even when a limit demands it.
	doomed = slices.DeleteFunc(doomed, func(meta Meta) bool { return meta.ID == metas[0].ID })
	if len(doomed) == 0 {
		return 0, nil
	}
	if len(keep) == 0 {
		keep = []Meta{metas[0]}
	}

	var errs []error
	removed := 0
	for _, meta := range doomed {
		if err := os.Remove(s.sessionPath(meta.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove session %s: %w", meta.ID, err))
			continue
		}
		removed++
	}
	errs = append(errs, s.writeIndex(keep))
	return removed, errors.Join(errs...)
}
