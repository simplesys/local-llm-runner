// Package metrics collects session metrics (elapsed time, turns, prompt and
// completion tokens, throughput) and formats the end-of-session summary.
//
// Token counts come from the usage field of API responses, never from
// parsing text output.
//
// Status: planned (FR-12 in docs/project_description.md).
package metrics
