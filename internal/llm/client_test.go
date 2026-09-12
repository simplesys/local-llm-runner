package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/llm"
)

// sseServer serves a recorded stream and records the request body.
func sseServer(t *testing.T, fixture string) (*httptest.Server, *[]byte) {
	t.Helper()
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = readAll(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &received
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	for {
		n, err := r.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

func newClient(t *testing.T, baseURL string) *llm.Client {
	t.Helper()
	client, err := llm.NewClient(&http.Client{}, baseURL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func request() llm.Request {
	return llm.Request{
		Model:    "qwen3",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "what is in go.mod?", ReasoningContent: "must not be sent"}},
	}
}

func TestClientStreamText(t *testing.T) {
	t.Parallel()

	server, received := sseServer(t, "testdata/stream_text.sse")
	client := newClient(t, server.URL+"/v1")

	var deltas []string
	completion, err := client.Stream(t.Context(), request(), llm.StreamHandler{
		OnContent: func(delta string) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if completion.Message.Content != "Hello, world" {
		t.Errorf("content = %q, want %q", completion.Message.Content, "Hello, world")
	}
	if strings.Join(deltas, "|") != "Hello|, world" {
		t.Errorf("deltas = %v, want [Hello , world]", deltas)
	}
	if completion.Usage.PromptTokens != 120 || completion.Usage.TotalTokens != 128 {
		t.Errorf("usage = %+v, want prompt 120 and total 128", completion.Usage)
	}
	if completion.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q", completion.FinishReason, "stop")
	}

	var sent map[string]any
	if err := json.Unmarshal(*received, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent["stream"] != true {
		t.Errorf("stream = %v, want true", sent["stream"])
	}
	options, ok := sent["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", sent["stream_options"])
	}
	if strings.Contains(string(*received), "must not be sent") {
		t.Error("the request carries reasoning_content, want it stripped")
	}
}

func TestClientStreamToolCalls(t *testing.T) {
	t.Parallel()

	server, _ := sseServer(t, "testdata/stream_tools.sse")
	client := newClient(t, server.URL+"/v1")

	var reported []llm.ToolCall
	completion, err := client.Stream(t.Context(), request(), llm.StreamHandler{
		OnToolCall: func(call llm.ToolCall) { reported = append(reported, call) },
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	calls := completion.Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("len(tool calls) = %d, want 2", len(calls))
	}
	if calls[0].Function.Name != "read_file" || calls[0].ID != "call_a" {
		t.Errorf("tool call 0 = %+v, want read_file/call_a", calls[0])
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("tool call arguments are not valid JSON: %v (%q)", err, calls[0].Function.Arguments)
	}
	if args.Path != "go.mod" {
		t.Errorf("arguments.path = %q, want %q", args.Path, "go.mod")
	}
	if calls[1].Function.Name != "list_dir" {
		t.Errorf("tool call 1 = %+v, want list_dir", calls[1])
	}
	if len(reported) != 2 {
		t.Errorf("OnToolCall calls = %d, want 2", len(reported))
	}
	if completion.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want %q", completion.FinishReason, "tool_calls")
	}
}

func TestClientStreamReasoning(t *testing.T) {
	t.Parallel()

	server, _ := sseServer(t, "testdata/stream_reasoning.sse")
	client := newClient(t, server.URL+"/v1")

	var reasoning strings.Builder
	completion, err := client.Stream(t.Context(), request(), llm.StreamHandler{
		OnReasoning: func(delta string) { reasoning.WriteString(delta) },
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	const wantReasoning = "The user asks about go.mod."
	if reasoning.String() != wantReasoning {
		t.Errorf("reported reasoning = %q, want %q", reasoning.String(), wantReasoning)
	}
	if completion.Message.ReasoningContent != wantReasoning {
		t.Errorf("message reasoning = %q, want %q", completion.Message.ReasoningContent, wantReasoning)
	}
	if strings.Contains(completion.Message.Content, "The user asks") {
		t.Errorf("content = %q, want it free of reasoning", completion.Message.Content)
	}
}

func TestClientStreamNilHandler(t *testing.T) {
	t.Parallel()

	server, _ := sseServer(t, "testdata/stream_tools.sse")
	client := newClient(t, server.URL+"/v1")
	if _, err := client.Stream(t.Context(), request(), llm.StreamHandler{}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
}

func TestClientStreamServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Failed to resolve model metadata"}`))
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL+"/v1")
	_, err := client.Stream(t.Context(), request(), llm.StreamHandler{})
	if err == nil {
		t.Fatal("Stream() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "Failed to resolve model metadata") {
		t.Errorf("Stream() error = %v, want it to quote the server message", err)
	}
}

func TestClientStreamTruncatedStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {oops\n"))
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL+"/v1")
	if _, err := client.Stream(t.Context(), request(), llm.StreamHandler{}); err == nil {
		t.Error("Stream() error = nil, want an error for a malformed stream")
	}
}

func TestClientStreamCanceled(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	client := newClient(t, server.URL+"/v1")
	ctx, cancel := context.WithCancel(t.Context())
	var once bool
	_, err := client.Stream(ctx, request(), llm.StreamHandler{
		OnContent: func(string) {
			if !once {
				once = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Stream() error = %v, want context.Canceled", err)
	}
}

func TestClientStreamRejectsIncompleteRequest(t *testing.T) {
	t.Parallel()

	client := newClient(t, "http://127.0.0.1:1/v1")
	tests := []struct {
		name string
		req  llm.Request
	}{
		{name: "empty model", req: llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}},
		{name: "no messages", req: llm.Request{Model: "qwen3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := client.Stream(t.Context(), tt.req, llm.StreamHandler{}); err == nil {
				t.Error("Stream() error = nil, want an error before any request")
			}
		})
	}
}

func TestNewClientRejectsTimeout(t *testing.T) {
	t.Parallel()

	if _, err := llm.NewClient(&http.Client{Timeout: time.Second}, "http://localhost:1234/v1"); err == nil {
		t.Error("NewClient() error = nil, want an error for a client with Timeout")
	}
	if _, err := llm.NewClient(nil, "http://localhost:1234/v1"); err == nil {
		t.Error("NewClient(nil) error = nil, want an error")
	}
	if _, err := llm.NewClient(&http.Client{}, "localhost:1234"); err == nil {
		t.Error("NewClient() error = nil, want an error for a relative URL")
	}
}
