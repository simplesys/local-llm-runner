package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Scanner buffer sizes for SSE: a single data line can hold a whole tool call
// argument list, so 64 KiB is not enough.
const (
	streamInitialBuffer = 64 << 10
	streamMaxBuffer     = 1 << 20
)

// doneMarker ends an SSE stream of chat completions.
const doneMarker = "[DONE]"

// DecodeStream reads an SSE stream of chat completion chunks and calls fn for
// every chunk in order. It returns nil when the stream ends with the [DONE]
// marker or with EOF, and the error from fn when fn fails.
func DecodeStream(r io.Reader, fn func(Chunk) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, streamInitialBuffer), streamMaxBuffer)

	for line := 0; scanner.Scan(); line++ {
		payload, ok := eventData(scanner.Text())
		if !ok {
			continue
		}
		if payload == doneMarker {
			return nil
		}
		var chunk Chunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode stream line %d (%s): %w", line+1, snippet(payload), err)
		}
		if err := fn(chunk); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return nil
}

// eventData extracts the payload of a data line and reports whether the line
// carries one. Comments, empty lines and other SSE fields are ignored.
func eventData(line string) (string, bool) {
	line = strings.TrimRight(line, "\r")
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(rest, " "), true
}

// snippet shortens a payload for error messages.
func snippet(payload string) string {
	const maxSnippet = 200
	if len(payload) <= maxSnippet {
		return payload
	}
	return payload[:maxSnippet] + "..."
}
