package agent

import (
	"fmt"
	"strings"
)

// PromptData is what the system prompt needs to know about the environment.
type PromptData struct {
	// Workspace is the directory the agent may change.
	Workspace string
	// OS is the operating system the commands run on.
	OS string
	// Shell is the interpreter of the shell tool.
	Shell string
	// SandboxLevel is the isolation in effect.
	SandboxLevel string
	// Instructions is the text of the project instruction files; it may be
	// empty.
	Instructions string
}

// BuildSystemPrompt renders the system message. It is deliberately short:
// a local model follows a few firm rules better than a long briefing.
func BuildSystemPrompt(data PromptData) string {
	var prompt strings.Builder
	prompt.WriteString("You are a coding agent working in a terminal on the user's machine.\n\n")
	fmt.Fprintf(&prompt, "Workspace: %s\n", data.Workspace)
	fmt.Fprintf(&prompt, "Operating system: %s\n", data.OS)
	fmt.Fprintf(&prompt, "Shell: %s\n", data.Shell)
	fmt.Fprintf(&prompt, "Sandbox: %s\n\n", data.SandboxLevel)
	prompt.WriteString(`Rules:
- Read files before you change them; never guess their content.
- Change files only with the tools, never by printing a patch for the user to apply.
- Stay inside the workspace. Paths are relative to it.
- Never commit, push, or touch git history or remotes; the user does that.
- After changing code, run the project's checks with the shell tool.
- Prefer one tool call at a time and check its result before the next step.
- Answer in the language the user writes in. Keep answers short and concrete.
- When the task cannot be done, say so and start the answer with "TASK IMPOSSIBLE:".
`)
	if instructions := strings.TrimSpace(data.Instructions); instructions != "" {
		prompt.WriteString("\nProject instructions:\n")
		prompt.WriteString(instructions)
		prompt.WriteString("\n")
	}
	return prompt.String()
}
