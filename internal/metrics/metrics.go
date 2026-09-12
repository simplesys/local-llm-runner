// Package metrics collects session metrics (elapsed time, turns, tool calls,
// prompt and completion tokens, throughput), renders the end-of-session
// summary and stores one file per session.
//
// Token counts come from the usage field of API responses, never from parsing
// text output.
package metrics

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Collector accumulates session metrics. It is safe for concurrent use
// because tool execution and rendering may run in different goroutines.
type Collector struct {
	now func() time.Time

	mu               sync.Mutex
	startedAt        time.Time
	turns            int
	toolCalls        int
	promptTokens     int
	completionTokens int
}

// NewCollector starts a collector. now is injected for tests; nil means
// time.Now.
func NewCollector(now func() time.Time) *Collector {
	if now == nil {
		now = time.Now
	}
	return &Collector{now: now, startedAt: now()}
}

// AddTurn records one model request with its token usage.
func (c *Collector) AddTurn(promptTokens, completionTokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns++
	c.promptTokens += promptTokens
	c.completionTokens += completionTokens
}

// AddToolCall records one executed tool call.
func (c *Collector) AddToolCall() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolCalls++
}

// StartedAt reports when the session began.
func (c *Collector) StartedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startedAt
}

// Snapshot returns the metrics accumulated so far.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	elapsed := c.now().Sub(c.startedAt)
	total := c.promptTokens + c.completionTokens
	snapshot := Snapshot{
		Elapsed:          elapsed,
		ElapsedSeconds:   elapsed.Seconds(),
		Turns:            c.turns,
		ToolCalls:        c.toolCalls,
		PromptTokens:     c.promptTokens,
		CompletionTokens: c.completionTokens,
		TotalTokens:      total,
	}
	if elapsed > 0 {
		snapshot.TokensPerSecond = float64(total) / elapsed.Seconds()
	}
	return snapshot
}

// Snapshot is an immutable view of session metrics. Elapsed is kept out of
// JSON: a duration in nanoseconds is unreadable in a report, so the file
// carries elapsed_seconds instead.
type Snapshot struct {
	Elapsed          time.Duration `json:"-"`
	ElapsedSeconds   float64       `json:"elapsed_seconds"`
	Turns            int           `json:"turns"`
	ToolCalls        int           `json:"tool_calls"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TotalTokens      int           `json:"total_tokens"`
	TokensPerSecond  float64       `json:"tokens_per_second"`
}

// separator frames the summary, as in the Bash prototype.
const separator = "──────────────────────────────────────────────────"

// WriteText writes the human-readable summary in the format the Bash
// prototype used, so that results stay comparable across versions.
func (s Snapshot) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s\n Метрики сессии LLM\n"+
		"  Затраты времени : %.1f с\n"+
		"  Ходов (turns)   : %d\n"+
		"  Prompt токены   : %d\n"+
		"  Completion      : %d\n"+
		"  Всего токены    : %d\n"+
		"  Скорость        : %.1f tok/s (с учётом размышлений/инструментов)\n%s\n",
		separator,
		s.ElapsedSeconds, s.Turns, s.PromptTokens, s.CompletionTokens, s.TotalTokens, s.TokensPerSecond,
		separator)
	if err != nil {
		return fmt.Errorf("write metrics summary: %w", err)
	}
	return nil
}
