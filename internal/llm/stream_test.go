package llm_test

import (
	"os"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/llm"
)

func collect(t *testing.T, input string) ([]llm.Chunk, error) {
	t.Helper()
	var chunks []llm.Chunk
	err := llm.DecodeStream(strings.NewReader(input), func(chunk llm.Chunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	return chunks, err
}

func TestDecodeStreamFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string
		wantChunks int
		wantText   string
		wantUsage  int
	}{
		{name: "plain text", file: "testdata/stream_text.sse", wantChunks: 5, wantText: "Hello, world", wantUsage: 128},
		{name: "tool calls", file: "testdata/stream_tools.sse", wantChunks: 7, wantUsage: 342},
		{name: "reasoning", file: "testdata/stream_reasoning.sse", wantChunks: 4, wantText: "It declares Go 1.27.", wantUsage: 102},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			chunks, err := collect(t, string(data))
			if err != nil {
				t.Fatalf("DecodeStream() error = %v", err)
			}
			if len(chunks) != tt.wantChunks {
				t.Fatalf("len(chunks) = %d, want %d", len(chunks), tt.wantChunks)
			}

			var text strings.Builder
			var usage int
			for _, chunk := range chunks {
				for _, choice := range chunk.Choices {
					text.WriteString(choice.Delta.Content)
				}
				if chunk.Usage != nil {
					usage = chunk.Usage.TotalTokens
				}
			}
			if got := text.String(); got != tt.wantText {
				t.Errorf("content = %q, want %q", got, tt.wantText)
			}
			if usage != tt.wantUsage {
				t.Errorf("total tokens = %d, want %d", usage, tt.wantUsage)
			}
		})
	}
}

func TestDecodeStreamIgnoresNonDataLines(t *testing.T) {
	t.Parallel()

	input := ": comment\nevent: message\nid: 7\nretry: 1000\n\r\ndata: {\"choices\":[]}\r\n\ndata: [DONE]\n"
	chunks, err := collect(t, input)
	if err != nil {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestDecodeStreamStopsAtDone(t *testing.T) {
	t.Parallel()

	input := "data: {\"choices\":[]}\n\ndata: [DONE]\n\ndata: {\"choices\":[]}\n"
	chunks, err := collect(t, input)
	if err != nil {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1: chunks after [DONE] must be ignored", len(chunks))
	}
}

func TestDecodeStreamEndsAtEOF(t *testing.T) {
	t.Parallel()

	if _, err := collect(t, "data: {\"choices\":[]}\n"); err != nil {
		t.Errorf("DecodeStream() error = %v, want nil for a stream that just ends", err)
	}
}

func TestDecodeStreamMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := collect(t, "data: {\"choices\":[]}\n\ndata: {oops\n")
	if err == nil {
		t.Fatal("DecodeStream() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("DecodeStream() error = %v, want it to name the line", err)
	}
}

func TestDecodeStreamHandlerError(t *testing.T) {
	t.Parallel()

	wantErr := errTest
	calls := 0
	err := llm.DecodeStream(strings.NewReader("data: {}\n\ndata: {}\n"), func(llm.Chunk) error {
		calls++
		return wantErr
	})
	if err != wantErr { //nolint:errorlint // the handler error is returned as is, by contract
		t.Errorf("DecodeStream() error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
}

func TestDecodeStreamLongLine(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 300_000)
	input := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + long + "\"}}]}\n\ndata: [DONE]\n"
	chunks, err := collect(t, input)
	if err != nil {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if got := len(chunks[0].Choices[0].Delta.Content); got != len(long) {
		t.Errorf("content length = %d, want %d", got, len(long))
	}
}

func FuzzDecodeStream(f *testing.F) {
	f.Add("data: {\"choices\":[]}\n\ndata: [DONE]\n")
	f.Add("data: [DONE]")
	f.Add("data:")
	f.Add(": comment\n\n")
	f.Fuzz(func(_ *testing.T, input string) {
		_ = llm.DecodeStream(strings.NewReader(input), func(llm.Chunk) error { return nil })
	})
}
