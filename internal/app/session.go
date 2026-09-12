package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/simplesys/locallm/internal/agent"
	"github.com/simplesys/locallm/internal/config"
	"github.com/simplesys/locallm/internal/llm"
	"github.com/simplesys/locallm/internal/lmstudio"
	"github.com/simplesys/locallm/internal/metrics"
	"github.com/simplesys/locallm/internal/sandbox"
	"github.com/simplesys/locallm/internal/session"
	"github.com/simplesys/locallm/internal/tools"
	"github.com/simplesys/locallm/internal/ui"
)

// goEnvTimeout bounds the lookup of the Go caches, which only informs the
// sandbox profile and must never hold up the session.
const goEnvTimeout = 5 * time.Second

// recordedMessage is how a conversation message is stored in the session log.
type recordedMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Reasoning  string         `json:"reasoning,omitempty"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

// runSession builds every dependency of a session and runs it.
func runSession(ctx context.Context, opts Options, cfg config.Config, console *ui.Console) (err error) {
	httpClient := newHTTPClient()
	api, err := lmstudio.NewClient(httpClient, cfg.BaseURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}

	models, err := api.Models(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w: is LM Studio running with the server started (lms server start)?", ErrBackend, err)
	}
	if cfg.ListModels {
		printModels(opts.Stdout, models)
		return nil
	}

	model, err := chooseModel(cfg, models, console)
	if err != nil {
		return err
	}
	lock, err := ensureLoaded(ctx, cfg, api, models, model, console)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { err = errors.Join(err, lock.Release()) }()
	}

	policy, err := sandbox.ParsePolicy(cfg.SandboxPolicy)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	box, err := sandbox.New(policy, cfg.Workspace, writablePaths(ctx))
	if err != nil {
		return fmt.Errorf("%w: pass --sandbox best-effort or --sandbox off to continue", err)
	}
	for _, note := range box.Notes() {
		console.Notice("sandbox: %s", note)
	}

	files, err := tools.NewFiles(cfg.Workspace, tools.FilesOptions{})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, files.Close()) }()

	registry := tools.NewRegistry()
	if err := registry.AddAll(files.Tools()); err != nil {
		return err
	}
	if err := registry.AddAll(files.SearchTools(tools.SearchOptions{})); err != nil {
		return err
	}
	shell, err := tools.NewShell(tools.ShellOptions{Workspace: cfg.Workspace, Sandbox: box})
	if err != nil {
		return err
	}
	if err := registry.Add(shell); err != nil {
		return err
	}

	collector := metrics.NewCollector(nil)
	store, writer := openSessionWriter(cfg, model, console)
	if writer != nil {
		defer func() { err = errors.Join(err, writer.Close()) }()
	}

	llmClient, err := llm.NewClient(httpClient, cfg.BaseURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}

	runner := &sessionRunner{
		cfg:       cfg,
		store:     store,
		console:   console,
		collector: collector,
		writer:    writer,
		model:     model,
		sandbox:   box,
		registry:  registry,
		client:    llmClient,
		api:       api,
	}
	if err := runner.newAgent(); err != nil {
		return err
	}

	mode := metrics.ModeInteractive
	if !cfg.Interactive() {
		mode = metrics.ModeOneShot
	}
	status, runErr := runner.run(ctx, opts)
	runner.report(mode, status)
	return runErr
}

// sessionRunner holds everything one session needs while it works.
type sessionRunner struct {
	cfg       config.Config
	console   *ui.Console
	collector *metrics.Collector
	store     *session.Store
	writer    *session.Writer
	model     string
	sandbox   sandbox.Sandbox
	registry  *tools.Registry
	client    *llm.Client
	api       *lmstudio.Client

	agent    *agent.Agent
	recorded int
	// input is the channel of the reader goroutine while an interactive
	// session runs; it is nil in one-shot mode.
	input <-chan inputLine
}

// newAgent builds the agent loop with the tools and limits of this session.
func (r *sessionRunner) newAgent() error {
	prompt := agent.BuildSystemPrompt(agent.PromptData{
		Workspace:    r.cfg.Workspace,
		OS:           runtime.GOOS,
		Shell:        strings.Join(tools.DefaultShell(), " "),
		SandboxLevel: string(r.sandbox.Level()),
		Instructions: r.projectInstructions(),
	})

	options := agent.Options{
		Model:        r.model,
		SystemPrompt: prompt,
		Tools:        r.registry,
		Limits: agent.Limits{
			MaxTurns:  r.cfg.MaxTurns,
			MaxTokens: r.cfg.MaxTokens,
			Deadline:  r.cfg.Timeout,
		},
		Events: r.handleEvent,
	}
	if !r.cfg.AutoApprove {
		options.Approve = r.approveFromInput
	}

	created, err := agent.New(r.client, options)
	if err != nil {
		return err
	}
	r.agent = created
	r.recorded = len(created.History())
	return nil
}

// projectInstructions reads the instruction file of the project, if any.
func (r *sessionRunner) projectInstructions() string {
	const maxInstructions = 8 << 10
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		//nolint:gosec // the file name is a fixed constant inside the workspace
		data, err := os.ReadFile(filepath.Join(r.cfg.Workspace, name))
		if err != nil {
			continue
		}
		if len(data) > maxInstructions {
			data = append(data[:maxInstructions], []byte("\n... instructions truncated")...)
		}
		r.console.Notice("project instructions: %s", name)
		return string(data)
	}
	return ""
}

// handleEvent feeds one agent event to the interface and the metrics.
func (r *sessionRunner) handleEvent(event agent.Event) {
	r.console.Handle(event)
	switch event.Kind {
	case agent.EventTurnDone:
		r.collector.AddTurn(event.Usage.PromptTokens, event.Usage.CompletionTokens)
	case agent.EventToolResult:
		r.collector.AddToolCall()
	case agent.EventContent, agent.EventReasoning, agent.EventToolStart, agent.EventNotice:
		// nothing to measure
	}
}

// run dispatches to the one-shot or the interactive mode and reports the
// status for the metrics file.
func (r *sessionRunner) run(ctx context.Context, opts Options) (string, error) {
	if !r.cfg.Interactive() {
		return r.runOneShot(ctx)
	}
	return r.runInteractive(ctx, opts)
}

// runOneShot executes a single task and returns.
func (r *sessionRunner) runOneShot(ctx context.Context) (string, error) {
	outcome, err := r.turn(ctx, r.cfg.Task)
	if err != nil {
		return metrics.StatusError, err
	}
	switch {
	case outcome.StopReason.LimitReached():
		return metrics.StatusLimit, fmt.Errorf("%w: %s", ErrLimit, outcome.StopReason)
	case outcome.StopReason == agent.StopCanceled:
		return metrics.StatusCanceled, errors.New("canceled")
	case outcome.StopReason == agent.StopDenied:
		return metrics.StatusCanceled, errors.New("the action was declined")
	default:
		return metrics.StatusOK, nil
	}
}

// turn runs one user input through the agent and records the new messages.
func (r *sessionRunner) turn(ctx context.Context, input string) (agent.Outcome, error) {
	r.record(session.TypeMessage, recordedMessage{Role: string(llm.RoleUser), Content: input})
	if r.writer != nil {
		if err := r.writer.SetTitle(input); err != nil {
			r.console.Warn("%v", err)
		}
	}
	r.recorded = len(r.agent.History())

	outcome, err := r.agent.Run(ctx, input)
	r.console.EndAnswer()
	r.recordNewMessages()
	return outcome, err
}

// recordNewMessages appends the messages produced since the last call.
func (r *sessionRunner) recordNewMessages() {
	history := r.agent.History()
	for _, message := range history[min(r.recorded, len(history)):] {
		entryType := session.TypeMessage
		if message.Role == llm.RoleTool {
			entryType = session.TypeToolResult
		}
		r.record(entryType, recordedMessage{
			Role:       string(message.Role),
			Content:    message.Content,
			Reasoning:  message.ReasoningContent,
			ToolCalls:  message.ToolCalls,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		})
	}
	r.recorded = len(history)
}

// record appends one entry to the session log.
func (r *sessionRunner) record(entryType string, payload any) {
	if r.writer == nil {
		return
	}
	if err := r.writer.Append(entryType, payload); err != nil {
		r.console.Warn("%v", err)
	}
}

// report prints the metrics and stores the session metrics file.
func (r *sessionRunner) report(mode, status string) {
	snapshot := r.collector.Snapshot()
	if err := snapshot.WriteText(r.console.Err); err != nil {
		r.console.Warn("%v", err)
	}
	r.record(session.TypeMetrics, snapshot)

	if r.cfg.MetricsDir == "" {
		return
	}
	sessionID := "no-session"
	if r.writer != nil {
		sessionID = r.writer.ID()
	}
	path, err := metrics.Write(r.cfg.MetricsDir, metrics.File{
		SessionID:  sessionID,
		Project:    r.cfg.Workspace,
		Model:      r.model,
		BaseURL:    r.cfg.BaseURL,
		Sandbox:    string(r.sandbox.Level()),
		Mode:       mode,
		Status:     status,
		StartedAt:  r.collector.StartedAt(),
		FinishedAt: time.Now(),
		Metrics:    snapshot,
	})
	if err != nil {
		r.console.Warn("%v", err)
		return
	}
	r.console.Notice("metrics: %s", path)
}

// openSessionWriter starts recording the session unless the history is off.
// A history that cannot be opened is a warning, never a failure: the user
// asked for work, not for bookkeeping.
func openSessionWriter(cfg config.Config, model string, console *ui.Console) (*session.Store, *session.Writer) {
	if cfg.NoSession || cfg.Retention.Disabled {
		return nil, nil
	}
	dir, err := config.SessionsDir()
	if err != nil {
		console.Warn("%v; the session will not be recorded", err)
		return nil, nil
	}
	store, err := session.NewStore(dir, nil)
	if err != nil {
		console.Warn("%v; the session will not be recorded", err)
		return nil, nil
	}
	if removed, err := store.Prune(session.Policy{
		MaxSessions: cfg.Retention.MaxSessions,
		MaxBytes:    cfg.Retention.MaxBytes,
		MaxAge:      cfg.Retention.MaxAge(),
	}); err != nil {
		console.Warn("%v", err)
	} else if removed > 0 {
		console.Notice("session history: %d old sessions removed", removed)
	}

	writer, err := store.Create(cfg.Workspace, model)
	if err != nil {
		console.Warn("%v; the session will not be recorded", err)
		return store, nil
	}
	return store, writer
}

// writablePaths lists the directories a build needs to write to outside the
// workspace, so that the sandbox does not break the project's own checks.
func writablePaths(ctx context.Context) []string {
	var paths []string
	if cache, err := os.UserCacheDir(); err == nil {
		paths = append(paths, cache)
	}
	if temp := os.TempDir(); temp != "" {
		paths = append(paths, temp)
	}
	ctx, cancel := context.WithTimeout(ctx, goEnvTimeout)
	defer cancel()
	for _, variable := range []string{"GOCACHE", "GOMODCACHE"} {
		//nolint:gosec // a fixed command with fixed arguments, none of it from the model
		output, err := exec.CommandContext(ctx, "go", "env", variable).Output()
		if err != nil {
			continue
		}
		if path := strings.TrimSpace(string(output)); filepath.IsAbs(path) {
			paths = append(paths, path)
		}
	}
	return paths
}
