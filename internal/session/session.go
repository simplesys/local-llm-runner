// Package session records the history of agent sessions: one append-only
// JSONL file per session plus an index, with retention by count, total size
// and age.
//
// The package stores payloads as raw JSON, so it does not depend on the LLM
// or agent types.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Entry types written to a session file.
const (
	TypeMeta       = "meta"
	TypeMessage    = "message"
	TypeToolCall   = "tool_call"
	TypeToolResult = "tool_result"
	TypeMetrics    = "metrics"
	TypeNote       = "note"
)

// Entry is one line of a session file. Data holds the payload exactly as it
// was written.
type Entry struct {
	At   time.Time       `json:"at"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Meta describes a session in the index.
type Meta struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Model     string    `json:"model"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Entries   int       `json:"entries"`
	Bytes     int64     `json:"bytes"`
}

// Policy limits how much history is kept. A zero limit disables that single
// limit.
type Policy struct {
	MaxSessions int
	MaxBytes    int64
	MaxAge      time.Duration
}

// maxTitleRunes bounds the title taken from the first user message.
const maxTitleRunes = 120

// idPattern is the shape of a session identifier. Anything that arrives from
// outside is matched against it before it is turned into a file name.
var idPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// newID builds an identifier from the start time and random suffix, so that
// sessions sort chronologically by name.
func newID(startedAt time.Time) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return startedAt.Format("20060102-150405") + "-" + hex.EncodeToString(suffix), nil
}

// validateID reports whether id is a well-formed session identifier.
func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("session id %q: want the form 20060102-150405-a1b2c3", id)
	}
	return nil
}

// title trims a user message down to a session title.
func title(text string) string {
	runes := []rune(text)
	for i, r := range runes {
		if r == '\n' {
			runes = runes[:i]
			break
		}
	}
	if len(runes) > maxTitleRunes {
		runes = runes[:maxTitleRunes]
	}
	return string(runes)
}
