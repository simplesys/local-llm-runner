package lmstudio_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/simplesys/locallm/internal/lmstudio"
)

// newModelsServer serves the recorded model listing and records the path it
// was asked for.
func newModelsServer(t *testing.T, status int, body []byte) (*httptest.Server, *string) {
	t.Helper()
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &requestedPath
}

func TestClientModels(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	server, requestedPath := newModelsServer(t, http.StatusOK, body)

	client, err := lmstudio.NewClient(server.Client(), server.URL+"/v1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	models, err := client.Models(t.Context())
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	if *requestedPath != "/api/v0/models" {
		t.Errorf("requested path = %q, want %q", *requestedPath, "/api/v0/models")
	}
	if len(models) != 4 {
		t.Fatalf("len(models) = %d, want 4", len(models))
	}

	coder := models[1]
	if coder.ID != "qwen3-coder-30b" {
		t.Errorf("models[1].ID = %q, want %q", coder.ID, "qwen3-coder-30b")
	}
	if !coder.Loaded() || !coder.SupportsTools() || !coder.Chattable() {
		t.Errorf("models[1] = %+v, want loaded, tool-capable and chattable", coder)
	}
	if got := coder.ContextLength(); got != 32768 {
		t.Errorf("models[1].ContextLength() = %d, want 32768", got)
	}
	if got := models[2].ContextLength(); got != 131072 {
		t.Errorf("models[2].ContextLength() = %d, want 131072", got)
	}
	if models[0].Chattable() {
		t.Errorf("models[0] = %+v, want an embeddings model", models[0])
	}
	if models[3].SupportsTools() {
		t.Errorf("models[3] = %+v, want no tool support", models[3])
	}
}

func TestClientModelsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":"boom"}`},
		{name: "malformed json", status: http.StatusOK, body: `{"data":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, _ := newModelsServer(t, tt.status, []byte(tt.body))
			client, err := lmstudio.NewClient(server.Client(), server.URL+"/v1")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if _, err := client.Models(t.Context()); err == nil {
				t.Error("Models() error = nil, want an error")
			}
		})
	}
}

func TestClientModelsCanceledContext(t *testing.T) {
	t.Parallel()

	server, _ := newModelsServer(t, http.StatusOK, []byte(`{"data":[]}`))
	client, err := lmstudio.NewClient(server.Client(), server.URL+"/v1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Models() error = %v, want context.Canceled", err)
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{name: "trailing v1 is stripped", baseURL: "http://localhost:1234/v1", want: "http://localhost:1234/api/v0/models"},
		{name: "trailing slash is stripped", baseURL: "http://localhost:1234/v1/", want: "http://localhost:1234/api/v0/models"},
		{name: "bare origin", baseURL: "http://localhost:1234", want: "http://localhost:1234/api/v0/models"},
		{name: "path prefix is kept", baseURL: "http://proxy/lm/v1", want: "http://proxy/lm/api/v0/models"},
		{name: "relative URL", baseURL: "localhost:1234", wantErr: true},
		{name: "empty URL", baseURL: "", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://localhost/v1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := lmstudio.NewClient(http.DefaultClient, tt.baseURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewClient(%q) error = nil, want an error", tt.baseURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient(%q) error = %v", tt.baseURL, err)
			}
			if got := client.ModelsURL(); got != tt.want {
				t.Errorf("ModelsURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClientRequiresHTTPClient(t *testing.T) {
	t.Parallel()
	if _, err := lmstudio.NewClient(nil, "http://localhost:1234/v1"); err == nil {
		t.Error("NewClient(nil, ...) error = nil, want an error")
	}
}

func TestFirstUsable(t *testing.T) {
	t.Parallel()

	loadedTool := lmstudio.Model{ID: "loaded", Type: lmstudio.TypeLLM, State: lmstudio.StateLoaded, Capabilities: []string{lmstudio.CapabilityToolUse}}
	coldTool := lmstudio.Model{ID: "cold", Type: lmstudio.TypeLLM, State: lmstudio.StateNotLoaded, Capabilities: []string{lmstudio.CapabilityToolUse}}
	noTools := lmstudio.Model{ID: "plain", Type: lmstudio.TypeLLM, State: lmstudio.StateLoaded}

	tests := []struct {
		name   string
		models []lmstudio.Model
		want   string
		wantOK bool
	}{
		{name: "prefers a loaded tool-capable model", models: []lmstudio.Model{coldTool, loadedTool}, want: "loaded", wantOK: true},
		{name: "falls back to a cold tool-capable model", models: []lmstudio.Model{noTools, coldTool}, want: "cold", wantOK: true},
		{name: "nothing usable", models: []lmstudio.Model{noTools}},
		{name: "empty listing", models: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := lmstudio.FirstUsable(tt.models)
			if ok != tt.wantOK {
				t.Fatalf("FirstUsable() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.want {
				t.Errorf("FirstUsable() = %q, want %q", got.ID, tt.want)
			}
		})
	}
}
