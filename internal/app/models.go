package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/simplesys/locallm/internal/config"
	"github.com/simplesys/locallm/internal/lmstudio"
	"github.com/simplesys/locallm/internal/ui"
)

// printModels writes the model listing for the user.
func printModels(w io.Writer, models []lmstudio.Model) {
	fmt.Fprintf(w, "%-40s %-10s %-6s %-9s %s\n", "MODEL", "STATE", "TOOLS", "CONTEXT", "QUANT")
	for _, model := range models {
		tools := "no"
		if model.SupportsTools() {
			tools = "yes"
		}
		state := model.State
		if state == "" {
			state = "unknown"
		}
		fmt.Fprintf(w, "%-40s %-10s %-6s %-9d %s\n",
			model.ID, state, tools, model.ContextLength(), model.Quantization)
	}
}

// chooseModel decides which model the session runs on.
func chooseModel(cfg config.Config, models []lmstudio.Model, console *ui.Console) (string, error) {
	if cfg.Model != "" {
		for _, model := range models {
			if model.ID == cfg.Model {
				if !model.SupportsTools() {
					console.Warn("model %s does not advertise tool support: file edits may not work", model.ID)
				}
				return model.ID, nil
			}
		}
		return "", fmt.Errorf("%w: model %q is not available, see --list-models", ErrBackend, cfg.Model)
	}

	if model, ok := lmstudio.FirstUsable(models); ok {
		console.Notice("model %s selected automatically", model.ID)
		return model.ID, nil
	}
	if !cfg.Interactive() {
		return "", fmt.Errorf("%w: no model with tool support is available: load one in LM Studio or pass --model", ErrBackend)
	}
	return askForModel(models, console)
}

// askForModel lets the user pick a model from the listing.
func askForModel(models []lmstudio.Model, console *ui.Console) (string, error) {
	choices := make([]lmstudio.Model, 0, len(models))
	for _, model := range models {
		if model.Chattable() {
			choices = append(choices, model)
		}
	}
	if len(choices) == 0 {
		return "", fmt.Errorf("%w: the server reports no conversational models", ErrBackend)
	}

	console.Notice("no model with tool support is loaded, pick one:")
	for index, model := range choices {
		tools := ""
		if !model.SupportsTools() {
			tools = " (no tool support)"
		}
		console.Notice("  %d) %s%s", index+1, model.ID, tools)
	}

	for range 3 {
		console.ShowPrompt()
		answer, ok, err := console.ReadLine()
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("no model selected")
		}
		number, convErr := strconv.Atoi(strings.TrimSpace(answer))
		if convErr == nil && number >= 1 && number <= len(choices) {
			return choices[number-1].ID, nil
		}
		console.Warn("enter a number between 1 and %d", len(choices))
	}
	return "", errors.New("no model selected")
}

// ensureLoaded makes the chosen model the only one in memory. It returns the
// lock that must be held for the rest of the session.
func ensureLoaded(ctx context.Context, cfg config.Config, api *lmstudio.Client, models []lmstudio.Model,
	model string, console *ui.Console,
) (*lmstudio.Lock, error) {
	if cfg.NoSwitch {
		return nil, nil
	}
	alreadyAlone := onlyModelLoaded(models, model)

	path, err := lmstudio.Locator{}.Find()
	if err != nil {
		if alreadyAlone {
			console.Warn("%v; the model is already loaded, so switching stays unavailable", err)
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %w: load the model in LM Studio or pass --no-switch", ErrBackend, err)
	}
	if alreadyAlone {
		return nil, nil
	}

	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	lock, err := lmstudio.AcquireLock(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: another instance is switching models: %w", ErrBackend, err)
	}

	switcher := lmstudio.Switcher{
		API:    api,
		CLI:    lmstudio.CLI{Path: path},
		Notify: func(message string) { console.Notice("%s", message) },
	}
	if err := switcher.Ensure(ctx, model, cfg.ContextLength); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %w", ErrBackend, err), lock.Release())
	}
	return lock, nil
}

// onlyModelLoaded reports whether the model is loaded and nothing else is.
func onlyModelLoaded(models []lmstudio.Model, model string) bool {
	loaded := false
	for _, candidate := range models {
		switch {
		case candidate.ID == model:
			loaded = candidate.Loaded()
		case candidate.Loaded() && candidate.Chattable():
			return false
		}
	}
	return loaded
}
