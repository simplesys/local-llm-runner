package lmstudio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Default timings of Switcher.
const (
	defaultPollInterval = 500 * time.Millisecond
	defaultWait         = 5 * time.Minute
)

// modelLister is the part of the native API client that Switcher needs.
type modelLister interface {
	Models(ctx context.Context) ([]Model, error)
}

// modelLoader is the part of the lms CLI that Switcher needs.
type modelLoader interface {
	Unload(ctx context.Context) error
	Load(ctx context.Context, model string, contextLength int) error
}

// ErrModelNotFound reports that the server does not know the model.
var ErrModelNotFound = errors.New("model not found on the server")

// Switcher makes sure the requested model is the only one in memory.
type Switcher struct {
	// API lists models and reports their state.
	API modelLister
	// CLI loads and unloads models.
	CLI modelLoader
	// Poll is the state poll interval; zero means 500ms.
	Poll time.Duration
	// Wait bounds how long to wait for the loaded state; zero means five
	// minutes.
	Wait time.Duration
	// Notify receives progress messages; it may be nil.
	Notify func(message string)
}

// Ensure makes model the only loaded model. It does nothing when the model is
// already loaded alone.
func (s Switcher) Ensure(ctx context.Context, model string, contextLength int) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model must not be empty")
	}
	models, err := s.API.Models(ctx)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	if !containsModel(models, model) {
		return fmt.Errorf("%w: %q, available: %s", ErrModelNotFound, model, availableList(models))
	}
	if onlyLoaded(models, model) {
		return nil
	}

	s.notify("unloading models")
	if err := s.CLI.Unload(ctx); err != nil {
		return fmt.Errorf("unload models: %w", err)
	}
	s.notify("loading " + model)
	if err := s.CLI.Load(ctx, model, contextLength); err != nil {
		return fmt.Errorf("load model %s: %w", model, err)
	}
	s.notify("waiting for " + model + " to become ready")
	return s.waitLoaded(ctx, model)
}

// waitLoaded polls the server until the model reports the loaded state.
func (s Switcher) waitLoaded(ctx context.Context, model string) error {
	poll := s.Poll
	if poll <= 0 {
		poll = defaultPollInterval
	}
	wait := s.Wait
	if wait <= 0 {
		wait = defaultWait
	}

	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		models, err := s.API.Models(ctx)
		if err == nil {
			for _, m := range models {
				if m.ID == model && m.Loaded() {
					s.notify(model + " is ready")
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("model %s did not become ready within %s: check the LM Studio logs", model, wait)
		case <-ticker.C:
		}
	}
}

func (s Switcher) notify(message string) {
	if s.Notify != nil {
		s.Notify(message)
	}
}

// containsModel reports whether the listing contains the model.
func containsModel(models []Model, model string) bool {
	for _, m := range models {
		if m.ID == model {
			return true
		}
	}
	return false
}

// onlyLoaded reports whether model is loaded and no other conversational
// model occupies memory.
func onlyLoaded(models []Model, model string) bool {
	wanted := false
	for _, m := range models {
		switch {
		case m.ID == model:
			wanted = m.Loaded()
		case m.Loaded() && m.Chattable():
			return false
		}
	}
	return wanted
}

// availableList renders model identifiers for error messages.
func availableList(models []Model) string {
	const maxListed = 20
	names := make([]string, 0, maxListed)
	for _, m := range models {
		if !m.Chattable() {
			continue
		}
		if len(names) == maxListed {
			names = append(names, "...")
			break
		}
		names = append(names, m.ID)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
