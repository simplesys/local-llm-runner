package lmstudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxResponseBytes caps how much of a model listing is read.
const maxResponseBytes = 4 << 20

// Client reads the LM Studio native API.
type Client struct {
	httpClient *http.Client
	origin     string
}

// NewClient builds a client from the OpenAI-compatible base URL (for example
// http://localhost:1234/v1); the native API lives next to it under /api/v0.
func NewClient(httpClient *http.Client, baseURL string) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("http client must not be nil")
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", baseURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("base URL %q: want absolute http or https URL", baseURL)
	}
	path := strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), "/v1")
	origin := parsed.Scheme + "://" + parsed.Host + path
	return &Client{httpClient: httpClient, origin: origin}, nil
}

// ModelsURL returns the endpoint the client reads the model listing from.
// It exists so that callers can name the address in diagnostics.
func (c *Client) ModelsURL() string { return c.origin + "/api/v0/models" }

// modelsResponse is the payload of GET /api/v0/models.
type modelsResponse struct {
	Data []modelPayload `json:"data"`
}

type modelPayload struct {
	ID                  string   `json:"id"`
	Type                string   `json:"type"`
	State               string   `json:"state"`
	Quantization        string   `json:"quantization"`
	MaxContextLength    int      `json:"max_context_length"`
	LoadedContextLength int      `json:"loaded_context_length"`
	Capabilities        []string `json:"capabilities"`
}

// Models returns every model known to the server, in the order the server
// reports them.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	endpoint := c.ModelsURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(body, 200))
		return nil, fmt.Errorf("get %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var payload modelsResponse
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s: %w", endpoint, err)
	}

	models := make([]Model, 0, len(payload.Data))
	for _, item := range payload.Data {
		models = append(models, Model(item))
	}
	return models, nil
}

// FirstUsable returns the model to work with when the user did not name one:
// a loaded model that supports tools, otherwise any model that supports
// tools. It reports false when nothing fits.
func FirstUsable(models []Model) (Model, bool) {
	for _, m := range models {
		if m.Chattable() && m.Loaded() && m.SupportsTools() {
			return m, true
		}
	}
	for _, m := range models {
		if m.Chattable() && m.SupportsTools() {
			return m, true
		}
	}
	return Model{}, false
}
