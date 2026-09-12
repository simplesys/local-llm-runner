// Package lmstudio manages models on a local LM Studio server: it lists
// them through the native API and loads or unloads them through the lms
// command-line tool that ships with LM Studio.
//
// The server has no REST endpoints for loading and unloading, and just-in-time
// loading does not evict models that were loaded by hand, so the CLI is the
// only way to guarantee that exactly one model occupies memory.
package lmstudio

import "slices"

// Model types reported by the LM Studio native API.
const (
	TypeLLM        = "llm"
	TypeVLM        = "vlm"
	TypeEmbeddings = "embeddings"
)

// Model states reported by the LM Studio native API.
const (
	StateLoaded    = "loaded"
	StateNotLoaded = "not-loaded"
)

// CapabilityToolUse marks models that can call tools.
const CapabilityToolUse = "tool_use"

// Model is a model as reported by the LM Studio native API.
type Model struct {
	ID                  string
	Type                string
	State               string
	Quantization        string
	MaxContextLength    int
	LoadedContextLength int
	Capabilities        []string
}

// Loaded reports whether the model currently occupies memory.
func (m Model) Loaded() bool { return m.State == StateLoaded }

// SupportsTools reports whether the model advertises the tool_use capability.
func (m Model) SupportsTools() bool {
	return slices.Contains(m.Capabilities, CapabilityToolUse)
}

// Chattable reports whether the model can be used for a conversation, as
// opposed to an embeddings model.
func (m Model) Chattable() bool {
	return m.Type == TypeLLM || m.Type == TypeVLM
}

// ContextLength returns the context window in effect: the length the model
// was loaded with when it is known, otherwise the maximum it supports.
func (m Model) ContextLength() int {
	if m.LoadedContextLength > 0 {
		return m.LoadedContextLength
	}
	return m.MaxContextLength
}
