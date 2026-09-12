package lmstudio_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/simplesys/locallm/internal/lmstudio"
)

// stubAPI reports a scripted sequence of model listings: the last listing is
// repeated once the script runs out.
type stubAPI struct {
	mu       sync.Mutex
	listings [][]lmstudio.Model
	calls    int
	err      error
}

func (s *stubAPI) Models(context.Context) ([]lmstudio.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	index := min(s.calls, len(s.listings)-1)
	s.calls++
	return s.listings[index], nil
}

// stubCLI records the order of the calls it receives.
type stubCLI struct {
	mu            sync.Mutex
	steps         []string
	loadedModel   string
	contextLength int
	loadErr       error
	unloadErr     error
}

func (s *stubCLI) Unload(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, "unload")
	return s.unloadErr
}

func (s *stubCLI) Load(_ context.Context, model string, contextLength int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, "load")
	s.loadedModel = model
	s.contextLength = contextLength
	return s.loadErr
}

func (s *stubCLI) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steps...)
}

func model(id, state string) lmstudio.Model {
	return lmstudio.Model{
		ID:           id,
		Type:         lmstudio.TypeLLM,
		State:        state,
		Capabilities: []string{lmstudio.CapabilityToolUse},
	}
}

func fastSwitcher(api *stubAPI, cli *stubCLI, notify func(string)) lmstudio.Switcher {
	return lmstudio.Switcher{
		API:    api,
		CLI:    cli,
		Poll:   time.Millisecond,
		Wait:   2 * time.Second,
		Notify: notify,
	}
}

func TestSwitcherEnsureAlreadyLoaded(t *testing.T) {
	t.Parallel()

	api := &stubAPI{listings: [][]lmstudio.Model{{
		model("wanted", lmstudio.StateLoaded),
		model("other", lmstudio.StateNotLoaded),
	}}}
	cli := &stubCLI{}
	if err := fastSwitcher(api, cli, nil).Ensure(t.Context(), "wanted", 0); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if steps := cli.recorded(); len(steps) != 0 {
		t.Errorf("cli calls = %v, want none", steps)
	}
}

func TestSwitcherEnsureSwitches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		first []lmstudio.Model
	}{
		{name: "another model is loaded", first: []lmstudio.Model{
			model("wanted", lmstudio.StateNotLoaded),
			model("other", lmstudio.StateLoaded),
		}},
		{name: "nothing is loaded", first: []lmstudio.Model{
			model("wanted", lmstudio.StateNotLoaded),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			api := &stubAPI{listings: [][]lmstudio.Model{
				tt.first,
				{model("wanted", lmstudio.StateLoaded)},
			}}
			cli := &stubCLI{}
			var messages []string
			switcher := fastSwitcher(api, cli, func(m string) { messages = append(messages, m) })
			if err := switcher.Ensure(t.Context(), "wanted", 8192); err != nil {
				t.Fatalf("Ensure() error = %v", err)
			}
			want := []string{"unload", "load"}
			if got := cli.recorded(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Errorf("cli calls = %v, want %v", got, want)
			}
			if cli.loadedModel != "wanted" || cli.contextLength != 8192 {
				t.Errorf("loaded %q with context %d, want %q with 8192", cli.loadedModel, cli.contextLength, "wanted")
			}
			if len(messages) == 0 {
				t.Error("Notify was never called, want progress messages")
			}
		})
	}
}

func TestSwitcherEnsureUnknownModel(t *testing.T) {
	t.Parallel()

	api := &stubAPI{listings: [][]lmstudio.Model{{model("other", lmstudio.StateLoaded)}}}
	cli := &stubCLI{}
	err := fastSwitcher(api, cli, nil).Ensure(t.Context(), "wanted", 0)
	if !errors.Is(err, lmstudio.ErrModelNotFound) {
		t.Fatalf("Ensure() error = %v, want ErrModelNotFound", err)
	}
	if steps := cli.recorded(); len(steps) != 0 {
		t.Errorf("cli calls = %v, want none", steps)
	}
}

func TestSwitcherEnsureLoadFails(t *testing.T) {
	t.Parallel()

	api := &stubAPI{listings: [][]lmstudio.Model{{model("wanted", lmstudio.StateNotLoaded)}}}
	cli := &stubCLI{loadErr: errors.New("no resources")}
	err := fastSwitcher(api, cli, nil).Ensure(t.Context(), "wanted", 0)
	if err == nil || !errors.Is(err, cli.loadErr) {
		t.Fatalf("Ensure() error = %v, want it to wrap the loader error", err)
	}
}

func TestSwitcherEnsureNeverBecomesReady(t *testing.T) {
	t.Parallel()

	api := &stubAPI{listings: [][]lmstudio.Model{{model("wanted", lmstudio.StateNotLoaded)}}}
	cli := &stubCLI{}
	switcher := lmstudio.Switcher{API: api, CLI: cli, Poll: time.Millisecond, Wait: 30 * time.Millisecond}
	if err := switcher.Ensure(t.Context(), "wanted", 0); err == nil {
		t.Fatal("Ensure() error = nil, want a timeout error")
	}
}

func TestSwitcherEnsureCanceled(t *testing.T) {
	t.Parallel()

	api := &stubAPI{listings: [][]lmstudio.Model{{model("wanted", lmstudio.StateNotLoaded)}}}
	cli := &stubCLI{}
	ctx, cancel := context.WithCancel(t.Context())
	switcher := lmstudio.Switcher{API: api, CLI: cli, Poll: 5 * time.Millisecond, Wait: time.Minute}

	done := make(chan error, 1)
	go func() { done <- switcher.Ensure(ctx, "wanted", 0) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Ensure() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure() did not return after the context was canceled")
	}
}

func TestSwitcherEnsureEmptyModel(t *testing.T) {
	t.Parallel()

	if err := (lmstudio.Switcher{}).Ensure(t.Context(), " ", 0); err == nil {
		t.Error("Ensure() error = nil, want an error")
	}
}
