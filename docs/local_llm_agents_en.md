# locallm — rules for local models

This file replaces `AGENTS.md` for local models (Qwen 3.8 27B, Ornith 1.5). It
is the only project-wide document you read.

Never open `docs/project_description.md`, `docs/technical_description.md`,
`docs/code_style.md`, `tasks/*/plan.md` or `docs/archives/`. They are large and
they are written for cloud models. Everything you need is here.

Project: `locallm`, a terminal coding agent for local LLMs, written in Go. Code,
identifiers, comments and godoc are in English.

## 1. How to work

1. Read the task file. Then read only the files the task names.
2. Write the plan in your **reply**, not only in your thinking: up to five
   numbered steps, each naming one file and one check. Your thinking is dropped
   after the turn; your reply is kept.
3. Do one step at a time: read the file, change it with `edit_file` or
   `write_file`, run the check, go to the next step.
4. When every step is done, run every check the task lists under "Done when",
   and `make check` last. Report the result.

The task is the contract: its "Do not" section is a hard limit, and its "Done
when" list is what proves the work.

Context rules:

- Never read the same file twice, unless you changed it since. Use what you
  already read.
- For a file longer than 400 lines use `read_file` with `offset` and `limit`, or
  `grep` with a narrow `path`.
- Never print a patch or code for the user to apply. Change files with tools.
- One tool call at a time; read its result before the next call.

## 2. Stop and ask

On any trigger below: stop. Do not guess, do not pick an option yourself, do not
"decide for now and fix later". Start the reply with `QUESTION:`, list the
options you see, and make no further tool calls until the developer answers. Ask
in the language the task is written in.

| # | Trigger |
|---|---|
| 1 | The task allows two readings, or offers a choice. |
| 2 | You are about to create a file, a package, an exported identifier, a flag or a slash command the task does not name. Unexported fields, helpers and variables inside a file the task names are yours to write — they are not a trigger. |
| 3 | You are about to change a file the task does not name. A `_test.go` file and `testdata/` next to a file the task names, and `go.mod` with `go.sum` updated by `go get` or `go mod tidy` for a module the task lists, count as named. |
| 4 | The task names a file, symbol or command that does not exist in the repository. |
| 5 | The task contradicts the code you have read. |
| 6 | You have weighed the same decision twice. |
| 7 | Three tool calls in a row and no file changed yet. |
| 8 | A test or `make check` fails twice after your fix. |
| 9 | `make check` fails in files the task does not name. |
| 10 | You need a module the task does not list, or any other change in `go.mod`. |
| 11 | You need to delete, rewrite or weaken code that already works, or to delete code the task does not mention, even if it looks dead or unused. Report it and ask; do not remove it yourself. |
| 12 | Your own output contains `I have to answer now.` Your thinking was cut off by the server. Do not start thinking again: write the plan you already have into the reply and continue from its first unfinished step. |

If the task cannot be done at all, start the reply with `TASK IMPOSSIBLE:` and
say why in one sentence.

## 3. Go rules

- Format with `make fmt` (gofumpt + goimports). Imports in three groups: standard
  library, external modules, this module.
- Packages you may import: the standard library, anything already imported in
  the repository, and the modules the task lists in its "Dependency to add in
  this step" section. Use the import path and the version the task gives,
  exactly as written.
- Never choose a library yourself, and never replace a library named in the task
  with your own standard-library version. A package that is neither already in
  the repository nor listed in the task — trigger 10.
- To add a module the task lists: `go get <path>@<version>`, then `go mod tidy`.
  Never edit `go.mod` or `go.sum` by hand.
- Happy path on the left: handle the error and return; no `else` after `return`.
- Wrap errors with `%w` and context: `fmt.Errorf("open %s: %w", name, err)`.
  Message in lower case, no trailing dot, no words "failed" or "error".
- Never ignore an error silently; `_ = f()` only with a comment saying why.
- Compare errors with `errors.Is` / `errors.As`, never with `==` or by string.
- No `panic`, `os.Exit` or `log.Fatal` outside `main`.
- `ctx context.Context` is the first parameter of every function that does I/O or
  waits. Never store a context in a struct field.
- No global mutable state and no `init()` with side effects. Dependencies arrive
  through the constructor. `os.Args`, `os.Getenv`, `os.Stdout` and `time.Now`
  are used only in `main` and `internal/app`; everywhere else they come in as
  parameters (`getenv func(string) string`, `now func() time.Time`,
  `io.Reader`, `io.Writer`).
- stdout carries only the result for the user; progress, diagnostics, errors and
  metrics go to stderr.
- Struct literals with field names. Magic numbers and strings become named
  constants. Octal literals as `0o644`.
- Names: `MixedCaps`, no underscores; `baseURL`, `userID`; no stuttering
  (`llm.Client`, not `llm.LLMClient`); getters without `Get`.
- Every exported identifier has a godoc comment that is a full sentence starting
  with its name. Every package has a package comment.
- File tools stay inside the workspace through `os.Root`. Files are created with
  `0o644`, directories with `0o755`.
- `//nolint` only on one line, with the linter name and a reason. Never weaken
  `.golangci.yml`.
- Tests: standard `testing`, table tests with `t.Run`, message prints the actual
  value first and the wanted value second, `t.TempDir()` and `t.Context()` for
  resources. Tests must not use the network or LM Studio.

## 4. Only what the task needs

Every line you change must be explainable by one step of your plan.

- Write the smallest code that satisfies the task. No options, settings, flags
  or flexibility the task does not ask for.
- Do not handle errors for cases that cannot happen.
- A new interface, generic or package only when the task cannot be done without
  it. Take interfaces, return concrete types; an interface is declared in the
  package that uses it and holds only the methods that package calls.
- Follow the style of the code around you, even where you would write it
  differently.
- Imports, variables and functions that your change made unused are deleted. No
  commented-out code. Code that was already dead before you — trigger 11.
- If your solution came out several times larger than the task needs, write it
  again, shorter. An experienced developer must not call it over-engineered.

## 5. Done means checked

Before you start a step, know what will prove it.

- New behaviour: a test for the bad input and a test for the good one, both
  pass.
- Bug fix: a test that reproduces the bug, and it passes after the fix.
- Refactoring: the tests are green before you start and green after.
- Golden files in `testdata/` are produced by running the test with `-update`,
  not written by hand.
- A step is done only when its check ran and passed. "It should work" is not a
  check.

## 6. Commands

| Command | Meaning |
|---|---|
| `make check` | format check, linters, tests with `-race`, `go mod tidy` check. Must be green before the task is done. |
| `make test` | tests only, faster while you iterate |
| `go test -race ./internal/<pkg>/` | tests of one package, fastest while you iterate |
| `go test -race -run TestName ./internal/<pkg>/` | one test; add `-update` to write the golden files of that test |
| `make fmt` | format the code |
| `make lint` | linters only |
| `make crossbuild` | cross-compile check for linux, windows and darwin |
| `go get <path>@<version>` | add a module the task lists, then run `go mod tidy` |

## 7. Never

- Never run `git commit`, `git push`, `git reset --hard`, `git rebase`, or create
  branches and tags. The developer does that.
- Never read or write `.env` or any other secret.
- Never change the Go version in `go.mod`, and never add a module the task does
  not list.
- Never "improve" neighbouring code, comments or formatting while you pass by.
- Never change or delete code written by the developer unless the task says so.

## 8. Notes for every local model

Your thinking does not survive the turn. Only the reply, the tool calls and the
tool results are sent back to the server on the next turn. Everything you worked
out silently is lost. So: decide in ten lines, write the decision into the reply,
and call a tool.

The server caps how long you may think, and the cap is reached faster than you
expect. A long silent deliberation is lost work — it is cut off in the middle and
never reaches the next turn. Short thinking plus a written plan always beats long
thinking.

Re-reading a file is lost work too. Every file you read stays in the context of
the whole run, so reading it again buys nothing and costs the same tokens twice.
