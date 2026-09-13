package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simplesys/locallm/internal/app"
	"github.com/simplesys/locallm/internal/config"
)

// fakeServer stands in for LM Studio: it lists models and replays prepared
// streaming answers.
type fakeServer struct {
	server       *httptest.Server
	modelsBody   string
	modelsStatus int
	replies      []string
	chatRequests []map[string]any
	// beforeReply is called with the number of chat requests received so
	// far, before the reply is written: it lets a test look at the state of
	// the session while the task is still running.
	beforeReply func(request int)
}

// loadedModel is a model listing with one loaded, tool-capable model.
const loadedModel = `{"object":"list","data":[
  {"id":"qwen3","object":"model","type":"llm","state":"loaded","quantization":"Q8_0",
   "max_context_length":32768,"loaded_context_length":32768,"capabilities":["tool_use"]}
]}`

// twoLoadedModels is a listing where two conversational models occupy memory
// at the same time.
const twoLoadedModels = `{"object":"list","data":[
  {"id":"qwen3","object":"model","type":"llm","state":"loaded","quantization":"Q8_0",
   "max_context_length":32768,"loaded_context_length":32768,"capabilities":["tool_use"]},
  {"id":"gemma","object":"model","type":"llm","state":"loaded","quantization":"Q4_K_M",
   "max_context_length":8192,"loaded_context_length":8192,"capabilities":["tool_use"]}
]}`

// modelWithoutTools is a listing where nothing supports tool calling.
const modelWithoutTools = `{"object":"list","data":[
  {"id":"plain","object":"model","type":"llm","state":"loaded","max_context_length":4096}
]}`

func newFakeServer(t *testing.T, modelsBody string, replies ...string) *fakeServer {
	t.Helper()
	fake := &fakeServer{modelsBody: modelsBody, modelsStatus: http.StatusOK, replies: replies}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(fake.modelsStatus)
			_, _ = w.Write([]byte(fake.modelsBody))
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.chatRequests = append(fake.chatRequests, body)
			if fake.beforeReply != nil {
				fake.beforeReply(len(fake.chatRequests))
			}
			index := min(len(fake.chatRequests)-1, len(fake.replies)-1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(fake.replies[index]))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeServer) baseURL() string { return f.server.URL + "/v1" }

// sseAnswer builds a stream with a plain text answer.
func sseAnswer(text string) string {
	chunk, err := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": "stop"}},
	})
	if err != nil {
		panic(err)
	}
	usage := `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`
	return "data: " + string(chunk) + "\n\ndata: " + usage + "\n\ndata: [DONE]\n"
}

// sseToolCall builds a stream that asks for one tool call.
func sseToolCall(id, name, arguments string) string {
	chunk, err := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{"tool_calls": []map[string]any{{
				"index":    0,
				"id":       id,
				"type":     "function",
				"function": map[string]string{"name": name, "arguments": arguments},
			}}},
			"finish_reason": "tool_calls",
		}},
	})
	if err != nil {
		panic(err)
	}
	usage := `{"choices":[],"usage":{"prompt_tokens":200,"completion_tokens":30,"total_tokens":230}}`
	return "data: " + string(chunk) + "\n\ndata: " + usage + "\n\ndata: [DONE]\n"
}

// run executes the application in an isolated configuration directory and
// workspace.
type runResult struct {
	stdout    string
	stderr    string
	err       error
	workspace string
	configDir string
}

func run(t *testing.T, fake *fakeServer, input string, args ...string) runResult {
	t.Helper()
	return runCLI(t, fake, input, append([]string{"--no-switch"}, args...)...)
}

// runSwitching runs with model management on and with no lms executable in
// reach, so that a session that tries to switch models fails instead of
// touching the real LM Studio.
func runSwitching(t *testing.T, fake *fakeServer, input string, args ...string) runResult {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PATH", filepath.Join(home, "bin"))
	t.Setenv("HOME", home)
	t.Setenv("LMSTUDIO_HOME", home)
	return runCLI(t, fake, input, args...)
}

func runCLI(t *testing.T, fake *fakeServer, input string, args ...string) runResult {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "config")
	workspace := t.TempDir()
	t.Setenv(config.EnvConfigDir, configDir)

	var stdout, stderr strings.Builder
	fullArgs := append([]string{
		"--base-url", fake.baseURL(),
		"--workspace", workspace,
		"--yes",
	}, args...)

	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args:    fullArgs,
		Getenv:  func(string) string { return "" },
		Stdin:   strings.NewReader(input),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return runResult{
		stdout: stdout.String(), stderr: stderr.String(), err: err,
		workspace: workspace, configDir: configDir,
	}
}

func TestOneShotAnswer(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("go.mod declares Go 1.27."))
	result := run(t, fake, "", "--task", "what is in go.mod?")

	if result.err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", result.err, result.stderr)
	}
	if !strings.Contains(result.stdout, "go.mod declares Go 1.27.") {
		t.Errorf("stdout = %q, want the answer", result.stdout)
	}
	if !strings.Contains(result.stderr, "Метрики сессии LLM") {
		t.Errorf("stderr = %q, want the metrics summary", result.stderr)
	}
	if !strings.Contains(result.stderr, "Всего токены    : 120") {
		t.Errorf("stderr = %q, want the token counts from usage", result.stderr)
	}
	if strings.Contains(result.stdout, "Метрики") || strings.Contains(result.stdout, "·") {
		t.Errorf("stdout = %q, want only the answer there", result.stdout)
	}
}

func TestOneShotUsesTools(t *testing.T) {
	fake := newFakeServer(t, loadedModel,
		sseToolCall("call_1", "read_file", `{"path":"go.mod"}`),
		sseAnswer("the module is example"),
	)
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv(config.EnvConfigDir, configDir)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	var stdout, stderr strings.Builder
	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args: []string{
			"--base-url", fake.baseURL(), "--workspace", workspace,
			"--no-switch", "--yes", "--task", "read go.mod",
		},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "→ read_file") {
		t.Errorf("stderr = %q, want the tool call echoed", stderr.String())
	}
	if !strings.Contains(stdout.String(), "the module is example") {
		t.Errorf("stdout = %q, want the final answer", stdout.String())
	}
	if len(fake.chatRequests) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(fake.chatRequests))
	}
	messages, ok := fake.chatRequests[1]["messages"].([]any)
	if !ok || len(messages) < 4 {
		t.Fatalf("second request messages = %v, want the tool result included", fake.chatRequests[1]["messages"])
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok || last["role"] != "tool" {
		t.Errorf("last message = %v, want the tool result", messages[len(messages)-1])
	}
	if content, _ := last["content"].(string); !strings.Contains(content, "module example") {
		t.Errorf("tool result = %q, want the file content", content)
	}
}

// TestProjectInstructionsPassedWhole guards the rule that the instruction
// file reaches the model as the developer wrote it, however long it is.
func TestProjectInstructionsPassedWhole(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("ok"))
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv(config.EnvConfigDir, configDir)
	workspace := t.TempDir()
	instructions := "# rules\n" +
		strings.Repeat("cover every change with tests.\n", 1000) +
		"the last rule: answer in the language of the user.\n"
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(instructions), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var stdout, stderr strings.Builder
	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args: []string{
			"--base-url", fake.baseURL(), "--workspace", workspace,
			"--no-switch", "--yes", "--task", "hi",
		},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", err, stderr.String())
	}
	if len(fake.chatRequests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(fake.chatRequests))
	}
	messages, ok := fake.chatRequests[0]["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %v, want the system message", fake.chatRequests[0]["messages"])
	}
	system, ok := messages[0].(map[string]any)
	if !ok || system["role"] != "system" {
		t.Fatalf("first message = %v, want the system message", messages[0])
	}
	prompt, _ := system["content"].(string)
	if !strings.Contains(prompt, "the last rule: answer in the language of the user.") {
		t.Errorf("system prompt ends with %q, want the whole instruction file", prompt[max(len(prompt)-80, 0):])
	}
	if strings.Contains(prompt, "truncated") {
		t.Errorf("system prompt ends with %q, want no truncation of the instructions",
			prompt[max(len(prompt)-80, 0):])
	}
}

func TestListModels(t *testing.T) {
	fake := newFakeServer(t, loadedModel)
	result := run(t, fake, "", "--list-models")

	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	if !strings.Contains(result.stdout, "qwen3") || !strings.Contains(result.stdout, "MODEL") {
		t.Errorf("stdout = %q, want the model table", result.stdout)
	}
	if len(fake.chatRequests) != 0 {
		t.Errorf("chat requests = %d, want none", len(fake.chatRequests))
	}
}

func TestNoToolCapableModelNonInteractive(t *testing.T) {
	fake := newFakeServer(t, modelWithoutTools, sseAnswer("never reached"))
	result := run(t, fake, "", "--task", "do something")

	if !errors.Is(result.err, app.ErrBackend) {
		t.Fatalf("Run() error = %v, want ErrBackend", result.err)
	}
	if !strings.Contains(result.err.Error(), "--model") {
		t.Errorf("Run() error = %v, want a hint about --model", result.err)
	}
}

func TestUnknownModel(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("never reached"))
	result := run(t, fake, "", "--task", "hi", "--model", "missing-model")

	if !errors.Is(result.err, app.ErrBackend) {
		t.Fatalf("Run() error = %v, want ErrBackend", result.err)
	}
}

func TestStartupKeepsTheLoadedModel(t *testing.T) {
	fake := newFakeServer(t, twoLoadedModels, sseAnswer("готово"))
	result := runSwitching(t, fake, "", "--task", "hi")

	if result.err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", result.err, result.stderr)
	}
	if len(fake.chatRequests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(fake.chatRequests))
	}
	if got := fake.chatRequests[0]["model"]; got != "qwen3" {
		t.Errorf("model = %v, want the loaded qwen3", got)
	}
	if strings.Contains(result.stderr, "unloading models") {
		t.Errorf("stderr = %q, want no model switching when no model is named", result.stderr)
	}
}

func TestNamedModelStillSwitches(t *testing.T) {
	fake := newFakeServer(t, twoLoadedModels, sseAnswer("never reached"))
	result := runSwitching(t, fake, "", "--task", "hi", "--model", "qwen3")

	if !errors.Is(result.err, app.ErrBackend) {
		t.Fatalf("Run() error = %v, want ErrBackend: naming a model must switch", result.err)
	}
	if !strings.Contains(result.err.Error(), "--no-switch") {
		t.Errorf("Run() error = %v, want the switching error about the missing lms", result.err)
	}
	if len(fake.chatRequests) != 0 {
		t.Errorf("chat requests = %d, want none", len(fake.chatRequests))
	}
}

func TestInteractiveWithoutLoadedModel(t *testing.T) {
	fake := newFakeServer(t, modelWithoutTools, sseAnswer("never reached"))
	result := run(t, fake, "hi\n/exit\n")

	if result.err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", result.err, result.stderr)
	}
	if len(fake.chatRequests) != 0 {
		t.Errorf("chat requests = %d, want none without a model", len(fake.chatRequests))
	}
	if !strings.Contains(result.stderr, "/model <id>") {
		t.Errorf("stderr = %q, want a hint about /model", result.stderr)
	}
}

func TestBackendUnavailable(t *testing.T) {
	fake := newFakeServer(t, loadedModel)
	fake.modelsStatus = http.StatusInternalServerError
	fake.modelsBody = `{"error":"boom"}`
	result := run(t, fake, "", "--task", "hi")

	if !errors.Is(result.err, app.ErrBackend) {
		t.Fatalf("Run() error = %v, want ErrBackend", result.err)
	}
	if !strings.Contains(result.err.Error(), "lms server start") {
		t.Errorf("Run() error = %v, want a hint about starting the server", result.err)
	}
}

func TestLimitReached(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseToolCall("call_1", "list_dir", `{}`))
	metricsDir := filepath.Join(t.TempDir(), "metrics")
	result := run(t, fake, "", "--task", "list everything", "--max-turns", "1", "--metrics-dir", metricsDir)

	if !errors.Is(result.err, app.ErrLimit) {
		t.Fatalf("Run() error = %v, want ErrLimit", result.err)
	}
	entries, err := os.ReadDir(metricsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("metrics files = %v, error = %v, want one file", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(metricsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	var file struct {
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode metrics file: %v", err)
	}
	if file.Status != "limit" || file.StopReason != "max_turns" {
		t.Errorf("metrics file = %+v, want the limit status and the budget that stopped the task", file)
	}
}

func TestMetricsFile(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("done"))
	metricsDir := filepath.Join(t.TempDir(), "metrics")
	result := run(t, fake, "", "--task", "hi", "--metrics-dir", metricsDir)

	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	entries, err := os.ReadDir(metricsDir)
	if err != nil {
		t.Fatalf("read metrics directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("metrics files = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(metricsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	var file struct {
		SessionID  string `json:"session_id"`
		Model      string `json:"model"`
		Mode       string `json:"mode"`
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
		Metrics    struct {
			Turns       int `json:"turns"`
			TotalTokens int `json:"total_tokens"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode metrics file: %v", err)
	}
	if file.Model != "qwen3" || file.Mode != "one-shot" || file.Status != "ok" || file.StopReason != "done" {
		t.Errorf("metrics file = %+v, want the model, mode, status and stop reason recorded", file)
	}
	if file.SessionID == "" || file.Metrics.Turns != 1 || file.Metrics.TotalTokens != 120 {
		t.Errorf("metrics file = %+v, want the session id and the counters", file)
	}
}

func TestSessionRecorded(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("recorded"))
	result := run(t, fake, "", "--task", "remember this")

	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	entries, err := os.ReadDir(filepath.Join(result.configDir, "sessions"))
	if err != nil {
		t.Fatalf("read session directory: %v", err)
	}
	var logs []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			logs = append(logs, entry.Name())
		}
	}
	if len(logs) != 1 {
		t.Fatalf("session logs = %v, want one", logs)
	}
	data, err := os.ReadFile(filepath.Join(result.configDir, "sessions", logs[0]))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"type":"meta"`, "remember this", "recorded", `"type":"metrics"`} {
		if !strings.Contains(text, want) {
			t.Errorf("session log = %s\nwant it to contain %q", text, want)
		}
	}
	if got := strings.Count(text, `"role":"user"`); got != 1 {
		t.Errorf("user messages in the log = %d, want 1:\n%s", got, text)
	}
}

// TestSessionLogWrittenDuringTask pins the behavior that matters when a task
// hangs, loops or dies: what the model has already done is in the log before
// the task ends, not only after it.
func TestSessionLogWrittenDuringTask(t *testing.T) {
	fake := newFakeServer(t, loadedModel,
		sseToolCall("call_1", "read_file", `{"path":"go.mod"}`),
		sseAnswer("the module is example"),
	)
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv(config.EnvConfigDir, configDir)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	var duringTask string
	fake.beforeReply = func(request int) {
		if request == 2 {
			duringTask = readSessionLog(t, configDir)
		}
	}

	var stdout, stderr strings.Builder
	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args: []string{
			"--base-url", fake.baseURL(), "--workspace", workspace,
			"--no-switch", "--yes", "--task", "read go.mod",
		},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", err, stderr.String())
	}

	for _, want := range []string{"read go.mod", "read_file", `"type":"tool_result"`, "module example"} {
		if !strings.Contains(duringTask, want) {
			t.Errorf("session log during the task = %s\nwant it to contain %q", duringTask, want)
		}
	}
	if strings.Contains(duringTask, "the module is example") {
		t.Errorf("session log during the task = %s\nwant no answer that the model has not given yet", duringTask)
	}
}

// readSessionLog returns the only session log in the configuration directory.
func readSessionLog(t *testing.T, configDir string) string {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session directory: %v", err)
	}
	var logs []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			logs = append(logs, entry.Name())
		}
	}
	if len(logs) != 1 {
		t.Fatalf("session logs = %v, want one", logs)
	}
	data, err := os.ReadFile(filepath.Join(dir, logs[0]))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	return string(data)
}

func TestNoSessionFlag(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("done"))
	result := run(t, fake, "", "--task", "hi", "--no-session")

	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	if _, err := os.Stat(filepath.Join(result.configDir, "sessions")); err == nil {
		t.Error("the session directory exists, want no history with --no-session")
	}
}

func TestInteractiveSession(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("привет"))
	result := run(t, fake, "привет\n/exit\n")

	if result.err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", result.err, result.stderr)
	}
	if !strings.Contains(result.stdout, "привет") {
		t.Errorf("stdout = %q, want the answer", result.stdout)
	}
	if !strings.Contains(result.stderr, "Метрики сессии LLM") {
		t.Errorf("stderr = %q, want the metrics summary", result.stderr)
	}
	if len(fake.chatRequests) != 1 {
		t.Errorf("chat requests = %d, want 1: /exit must not reach the model", len(fake.chatRequests))
	}
}

func TestInteractiveSlashCommands(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("ok"))
	result := run(t, fake, "/help\n/models\n/metrics\n/nonsense\nhi\n/clear\n/exit\n")

	if result.err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", result.err, result.stderr)
	}
	for _, want := range []string{"/help", "MODEL", "Метрики сессии LLM", "unknown command /nonsense", "conversation cleared"} {
		if !strings.Contains(result.stderr, want) {
			t.Errorf("stderr does not contain %q:\n%s", want, result.stderr)
		}
	}

	entries, err := os.ReadDir(filepath.Join(result.configDir, "sessions"))
	if err != nil {
		t.Fatalf("read session directory: %v", err)
	}
	logs := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			logs++
		}
	}
	if logs != 2 {
		t.Errorf("session logs = %d, want 2: /clear starts a new session", logs)
	}
}

func TestVersionAndHelp(t *testing.T) {
	fake := newFakeServer(t, loadedModel)

	version := run(t, fake, "", "--version")
	if version.err != nil {
		t.Fatalf("Run(--version) error = %v", version.err)
	}
	if !strings.Contains(version.stdout, "locallm test") {
		t.Errorf("stdout = %q, want the version", version.stdout)
	}

	help := run(t, fake, "", "--help")
	if help.err != nil {
		t.Fatalf("Run(--help) error = %v", help.err)
	}
	for _, want := range []string{"-task", "-model", "Exit codes"} {
		if !strings.Contains(help.stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", help.stdout, want)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	fake := newFakeServer(t, loadedModel)

	for _, args := range [][]string{{"--nope"}, {"positional"}, {"--sandbox", "wrong"}} {
		result := run(t, fake, "", args...)
		if !errors.Is(result.err, app.ErrUsage) {
			t.Errorf("Run(%v) error = %v, want ErrUsage", args, result.err)
		}
	}
}

func TestCorruptSettingsAreReported(t *testing.T) {
	fake := newFakeServer(t, loadedModel, sseAnswer("done"))
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	t.Setenv(config.EnvConfigDir, configDir)

	var stdout, stderr strings.Builder
	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args: []string{
			"--base-url", fake.baseURL(), "--workspace", t.TempDir(),
			"--no-switch", "--yes", "--task", "hi",
		},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want the program to work with defaults", err)
	}
	if !strings.Contains(stderr.String(), "using defaults") {
		t.Errorf("stderr = %q, want a warning about the settings file", stderr.String())
	}
}

func TestWorkspaceMustExist(t *testing.T) {
	fake := newFakeServer(t, loadedModel)
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv(config.EnvConfigDir, filepath.Join(t.TempDir(), "config"))

	var stdout, stderr strings.Builder
	err := app.Run(t.Context(), app.Options{
		Version: "test",
		Args:    []string{"--base-url", fake.baseURL(), "--workspace", missing, "--task", "hi"},
		Getenv:  func(string) string { return "" },
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout, Stderr: &stderr,
	})
	if !errors.Is(err, app.ErrUsage) {
		t.Errorf("Run() error = %v, want ErrUsage", err)
	}
	_ = fmt.Sprint(stdout.String(), stderr.String())
}
