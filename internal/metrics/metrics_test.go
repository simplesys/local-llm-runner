package metrics_test

import (
	"bytes"
	"flag"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/metrics"
)

var update = flag.Bool("update", false, "update golden files")

// fakeClock advances only when the test says so.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCollectorSnapshot(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)}
	collector := metrics.NewCollector(clock.Now)
	collector.AddTurn(1000, 200)
	collector.AddTurn(2000, 300)
	collector.AddTurn(3000, 500)
	collector.AddToolCall()
	collector.AddToolCall()
	clock.advance(10 * time.Second)

	got := collector.Snapshot()
	want := metrics.Snapshot{
		Elapsed:          10 * time.Second,
		ElapsedSeconds:   10,
		Turns:            3,
		ToolCalls:        2,
		PromptTokens:     6000,
		CompletionTokens: 1000,
		TotalTokens:      7000,
		TokensPerSecond:  700,
	}
	if got != want {
		t.Errorf("Snapshot() = %+v, want %+v", got, want)
	}
}

func TestCollectorZeroElapsed(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(0, 0)}
	collector := metrics.NewCollector(clock.Now)
	collector.AddTurn(10, 10)

	got := collector.Snapshot()
	if got.TokensPerSecond != 0 {
		t.Errorf("TokensPerSecond = %v, want 0 when no time has passed", got.TokensPerSecond)
	}
}

func TestCollectorConcurrent(t *testing.T) {
	t.Parallel()

	collector := metrics.NewCollector(nil)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			collector.AddTurn(10, 5)
			collector.AddToolCall()
		})
	}
	wg.Wait()

	got := collector.Snapshot()
	if got.Turns != 100 || got.ToolCalls != 100 || got.PromptTokens != 1000 || got.CompletionTokens != 500 {
		t.Errorf("Snapshot() = %+v, want 100 turns, 100 tool calls, 1000 prompt and 500 completion tokens", got)
	}
}

func TestSnapshotWriteText(t *testing.T) {
	t.Parallel()

	snapshot := metrics.Snapshot{
		Elapsed:          12300 * time.Millisecond,
		ElapsedSeconds:   12.3,
		Turns:            3,
		PromptTokens:     5120,
		CompletionTokens: 870,
		TotalTokens:      5990,
		TokensPerSecond:  487.0,
	}
	var buf bytes.Buffer
	if err := snapshot.WriteText(&buf); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}

	const golden = "testdata/summary.golden"
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("WriteText() =\n%s\nwant\n%s", buf.String(), want)
	}
}
