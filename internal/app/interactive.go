package app

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/simplesys/locallm/internal/agent"
	"github.com/simplesys/locallm/internal/metrics"
	"github.com/simplesys/locallm/internal/session"
	"github.com/simplesys/locallm/internal/ui"
)

// doublePressWindow is how long a second Ctrl+C still counts as "quit".
const doublePressWindow = 2 * time.Second

// runInteractive reads user input until the user leaves or input ends.
func (r *sessionRunner) runInteractive(ctx context.Context, opts Options) (string, error) {
	model := r.model
	if model == "" {
		model = "not selected"
	}
	r.console.Notice("model %s · workspace %s · sandbox %s", model, r.cfg.Workspace, r.sandbox.Level())
	r.console.Notice("type /help for commands, /exit to quit")

	input := readLines(r.console)
	r.input = input
	defer func() { r.input = nil }()
	var interrupts *interrupter
	if opts.HandleInterrupts {
		interrupts = newInterrupter(r.console)
		defer interrupts.close()
	}

	status := metrics.StatusOK
	for {
		r.console.ShowPrompt()
		select {
		case <-ctx.Done():
			return status, nil
		case line, ok := <-input:
			if !ok {
				r.console.Notice("input ended, leaving")
				return status, nil
			}
			if line.err != nil {
				return metrics.StatusError, line.err
			}
			if strings.TrimSpace(line.text) == "" {
				continue
			}
			if command, isCommand := ui.ParseSlash(line.text); isCommand {
				quit, err := r.runCommand(ctx, command)
				if err != nil {
					r.console.Warn("%v", err)
				}
				if quit {
					return status, nil
				}
				continue
			}

			if r.agent == nil {
				r.console.Warn("no model is selected: pick one with /model <id>")
				continue
			}

			turnCtx, cancel := context.WithCancel(ctx)
			if interrupts != nil {
				interrupts.attach(cancel)
			}
			outcome, err := r.turn(turnCtx, line.text)
			cancel()
			if interrupts != nil {
				interrupts.detach()
				if interrupts.quitRequested() {
					return status, nil
				}
			}
			switch {
			case err != nil:
				r.console.Warn("%v", err)
				status = metrics.StatusError
			case outcome.StopReason.LimitReached():
				r.console.Warn("stopped: %s; raise the limit or start a new task", outcome.StopReason)
				status = metrics.StatusLimit
			case outcome.StopReason == agent.StopCanceled:
				r.console.Notice("turn canceled")
			}
		}
	}
}

// runCommand executes a slash command and reports whether the session ends.
func (r *sessionRunner) runCommand(ctx context.Context, command ui.SlashCommand) (bool, error) {
	switch command.Name {
	case "help":
		r.printHelp()
	case "exit", "quit":
		return true, nil
	case "clear":
		return false, r.clear()
	case "models":
		models, err := r.api.Models(ctx)
		if err != nil {
			return false, err
		}
		printModels(r.console.Err, models)
	case "model":
		if command.Args == "" {
			models, err := r.api.Models(ctx)
			if err != nil {
				return false, err
			}
			printModels(r.console.Err, models)
			if r.model == "" {
				r.console.Notice("no model is selected; pick one with /model <id>")
				return false, nil
			}
			r.console.Notice("current model: %s; switch with /model <id>", r.model)
			return false, nil
		}
		return false, r.switchModel(ctx, command.Args)
	case "metrics":
		snapshot := r.collector.Snapshot()
		if err := snapshot.WriteText(r.console.Err); err != nil {
			return false, err
		}
	default:
		r.console.Warn("unknown command /%s", command.Name)
		r.printHelp()
	}
	return false, nil
}

func (r *sessionRunner) printHelp() {
	for _, line := range []string{
		"/help            show this help",
		"/model [id]      list models or switch to one",
		"/models          list the models of the server",
		"/metrics         print the metrics of this session",
		"/clear           forget the conversation and start a new session",
		"/exit, /quit     leave",
	} {
		r.console.Notice("%s", line)
	}
}

// clear forgets the conversation and starts a new session log.
func (r *sessionRunner) clear() error {
	if r.agent != nil {
		r.agent.Reset()
		r.recorded = len(r.agent.History())
	}

	if r.writer == nil || r.store == nil {
		r.console.Notice("conversation cleared")
		return nil
	}
	if err := r.writer.Close(); err != nil {
		return err
	}
	writer, err := r.store.Create(r.cfg.Workspace, r.model)
	if err != nil {
		r.writer = nil
		return err
	}
	r.writer = writer
	r.console.Notice("conversation cleared, new session %s", writer.ID())
	return nil
}

// switchModel unloads the current model and loads another one, keeping the
// conversation.
func (r *sessionRunner) switchModel(ctx context.Context, name string) error {
	models, err := r.api.Models(ctx)
	if err != nil {
		return err
	}
	cfg := r.cfg
	cfg.Model = name
	target, err := chooseModel(cfg, models, r.console)
	if err != nil {
		return err
	}
	lock, err := ensureLoaded(ctx, cfg, r.api, models, target, r.console)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() {
			if releaseErr := lock.Release(); releaseErr != nil {
				r.console.Warn("%v", releaseErr)
			}
		}()
	}

	r.model = target
	if r.agent == nil {
		if err := r.newAgent(); err != nil {
			return err
		}
	} else {
		r.agent.SetModel(target)
	}
	if r.writer != nil {
		if err := r.writer.SetModel(target); err != nil {
			r.console.Warn("%v", err)
		}
	}
	r.record(session.TypeNote, map[string]string{"event": "model switched", "model": target})
	r.console.Notice("model switched to %s", target)
	return nil
}

// inputLine is one line of user input or the error that ended the input.
type inputLine struct {
	text string
	err  error
}

// approveFromInput confirms a mutating tool call through the single reader
// goroutine, so that nothing else reads the terminal at the same time.
func (r *sessionRunner) approveFromInput(ctx context.Context, call agent.ToolRequest) (bool, error) {
	if r.input == nil {
		return r.console.Approve(ctx, call)
	}
	r.console.AskApproval(call)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case line, ok := <-r.input:
		if !ok {
			return false, nil
		}
		if line.err != nil {
			return false, line.err
		}
		return ui.Approved(line.text), nil
	}
}

// readLines reads user input in its own goroutine so that the main loop can
// also react to signals and to context cancellation. The goroutine ends when
// input ends, which for a terminal happens when the process exits.
func readLines(console *ui.Console) <-chan inputLine {
	lines := make(chan inputLine)
	go func() {
		defer close(lines)
		for {
			text, ok, err := console.ReadLine()
			if err != nil {
				lines <- inputLine{err: err}
				return
			}
			if !ok {
				return
			}
			lines <- inputLine{text: text}
		}
	}()
	return lines
}

// interrupter turns Ctrl+C into "stop this turn", and into "quit" when it is
// pressed twice in a row.
type interrupter struct {
	console *ui.Console
	signals chan os.Signal
	done    chan struct{}

	mu       sync.Mutex
	cancel   context.CancelFunc
	lastSeen time.Time
	quit     bool
}

func newInterrupter(console *ui.Console) *interrupter {
	handler := &interrupter{
		console: console,
		signals: make(chan os.Signal, 1),
		done:    make(chan struct{}),
	}
	signal.Notify(handler.signals, os.Interrupt)
	go handler.loop()
	return handler
}

func (i *interrupter) loop() {
	for {
		select {
		case <-i.done:
			return
		case <-i.signals:
			i.handle()
		}
	}
}

func (i *interrupter) handle() {
	i.mu.Lock()
	repeated := time.Since(i.lastSeen) < doublePressWindow
	i.lastSeen = time.Now()
	cancel := i.cancel
	if cancel == nil || repeated {
		i.quit = true
	}
	i.mu.Unlock()

	switch {
	case cancel == nil:
		i.console.Notice("interrupted; press Enter to leave")
	case repeated:
		i.console.Notice("interrupted twice; leaving")
		cancel()
	default:
		i.console.Notice("stopping the current turn, press Ctrl+C again to leave")
		cancel()
	}
}

// attach registers the cancel function of the turn that is starting.
func (i *interrupter) attach(cancel context.CancelFunc) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancel = cancel
}

// detach forgets the cancel function of the finished turn.
func (i *interrupter) detach() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancel = nil
}

// quitRequested reports whether the user asked to leave.
func (i *interrupter) quitRequested() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.quit
}

func (i *interrupter) close() {
	signal.Stop(i.signals)
	close(i.done)
}
