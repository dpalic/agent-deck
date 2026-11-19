# Harness identity injection

Every session agent-deck launches is told, at runtime and in a form the model
can read, that it runs inside agent-deck, what its own session identity is,
and how to use the `agent-deck` CLI from inside. This works in every harness
(`claude`, `codex`, `pi`, `omp`, `gemini`) and, through an environment
variable, in custom `--cmd` sessions too.

## What the session learns

A short block (well under 40 lines) is generated from the session's database
record on every start and restart:

```
# agent-deck session context
You are running inside agent-deck, a terminal session manager for AI coding agents. ...

## This session (snapshot from the agent-deck database at launch)
- session id: 8c211446-1789219548
- title: after-claude
- tool: claude
- group: after
- profile: default
- account: (none)
- parent session id: (none: this is a root session)
- project path: /path/to/project

## agent-deck CLI (flags go BEFORE positional arguments)
- `agent-deck session current --json` — this session's full, current metadata ...
- `agent-deck session send <id-or-title> "message"` — ...
- `agent-deck session output <id-or-title>` — ...
- `agent-deck launch <path> -t "Title" -c claude --message "prompt"` — ...
- `agent-deck session children --json` — ...
- `agent-deck inbox drain --json <id>` — ...
- `agent-deck list --json` — ...

## Completion sentinel
When a task you were given by a parent is fully done, end your final message with exactly one line:
===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>
...

## Working style
Use a dynamic Workflow (fan-out agents) for every set of independent steps; serial only what must be ordered.

This block is at $AGENTDECK_IDENTITY_FILE and is regenerated on every start/restart.
```

The "Working style" line is the fan-out hint. It is on by default for
Claude-family sessions (claude and custom tools wrapping it) and is also
added to the `primer` block, followed there by the same "This block only
adds context ... take precedence over it." line the full block ends with.
Other tools never get it: "dynamic Workflow" is a Claude Code tool.
`[launch] fanout_hint = false` removes it from both levels and leaves the
rest of the block as it was. The skill's worker prompt conventions carry
the same line, and `launch-subagent.sh` appends it only for a claude child
that gets no identity block.

The block is written to `<data-dir>/agent-deck/runtime/identity/<session-id>/identity.md`
(plus a `GEMINI.md` copy for gemini). Its path is exported to the session as
`AGENTDECK_IDENTITY_FILE`. It is a launch-time snapshot; the existing
`AGENTDECK_*` environment variables and `agent-deck session current --json`
remain the machine-readable source of truth and always reflect the database.

Nothing is ever written into the project directory: no `AGENTS.md`,
`CLAUDE.md` or `GEMINI.md` is created or edited there.

## Mechanism per harness

| Tool | How the block reaches the model | Restart / resume | Not covered |
|------|-------------------------------|------------------|-------------|
| `claude` | `--append-system-prompt-file <file>` (Claude's own prompt is kept) | yes, incl. `--resume` and forks | custom `[claude].command` wrappers, `claude <subcommand>` passthrough: env var only |
| `codex` | `-c developer_instructions="<block>"`, the block inlined as a TOML basic string (lossless for any title/path); a `developer_instructions` you configured in that launch's `CODEX_HOME/config.toml` is placed first so it is never replaced. `model_instructions_file` would replace the base prompt and is not used | yes, incl. `resume` / `fork` | custom codex commands (`-c "codex --flag"`): env var only |
| `pi` | `--append-system-prompt <file>` | yes, incl. `session fork` | |
| `omp` | `--append-system-prompt <file>` (Oh My Pi, same shape as `pi`; see `docs/cli-reference.md` in the [oh-my-pi repo](https://github.com/can1357/oh-my-pi)) | yes, incl. `session fork` | |
| `gemini` | `--include-directories <dir>` with `GEMINI.md` inside, loaded as project memory next to the project's own | yes, incl. `--resume` | see folder trust below |
| `opencode` | `OPENCODE_CONFIG_CONTENT={"instructions":["<file>"]}`: opencode merges this config layer into yours and unions every layer's `instructions`, so your own `opencode.json` instructions stay | yes | an `OPENCODE_CONFIG_CONTENT` you already export is left alone (it holds one JSON value) and the session gets the env var only; with an opencode 2 shared service that was started elsewhere, that service's config applies |
| `copilot` | `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` gains `<session dir>/copilot`, which holds the block as `AGENTS.md`; appended with a comma to any value you set | yes | |
| custom `--cmd`, cursor, crush, hermes, deepseek | `AGENTDECK_IDENTITY_FILE` in the environment | yes | no prompt injection; the command decides whether to read the file. hermes' only knob (`HERMES_EPHEMERAL_SYSTEM_PROMPT`) replaces your `agent.system_prompt`, cursor-agent only takes a local plugin dir (`--plugin-dir`) that team policy can block, and crush has no additive option, so none is used |
| plain shell session (`shell`, no command) | `AGENTDECK_IDENTITY_FILE` exported into the pane shell by one line typed at start, together with `AGENTDECK_INSTANCE_ID`, `AGENTDECK_PROFILE`, `AGENTDECK_TOOL` and `AGENTDECK_TITLE` (the pane shell was already running when tmux's session environment was set, so an export is the only way to reach it) | yes | the line is visible in the pane; it is space-prefixed so a history that ignores such lines skips it |

SSH (`--ssh`) and Docker-sandboxed sessions get the block too. Their
harness runs where the controller's file is not visible, so the launch
command carries the block itself:

- claude, pi and omp get it as inline text, `--append-system-prompt "<block>"`
  (decoded by the remote/container shell from a single-line, shell-quoted
  `printf '%b'` argument, so quotes, backticks, `$()`, newlines and unicode
  in titles stay literal);
- codex gets the same inline `-c developer_instructions=...` override as a
  local session. Over SSH, the remote's own codex config is not readable from
  the controller, so a `developer_instructions` set in the remote's
  `config.toml` is not merged and is overridden for that spawn (use
  `--no-identity` for such a session). A sandbox mounts this host's codex
  home, so it merges as a local spawn does;
- before the harness starts, the same command writes the file where the
  harness runs, atomically (temp file + `mv`), to
  `$XDG_RUNTIME_DIR/agent-deck/identity/<session-id>/identity.md` (falling
  back to `~/.local/share/agent-deck/identity/<session-id>/`, directory mode 0700, never the project
  dir) and exports `AGENTDECK_IDENTITY_FILE` to it, so env-only tools get a
  path that exists there. If that write fails the variable is left unset and
  the launch goes on; the inline flags do not depend on it;
- the whole SSH payload runs under `bash -c` on the remote (it already did
  when an account or wrapper was set), so a fish or csh login shell there
  works; the remote needs `bash`. Known limit: a remote with only `sh`
  (Alpine, busybox images) cannot start SSH sessions this way; install bash
  there;
- the identity payload of an SSH/sandboxed launch is capped at 8 KB so the
  pane command stays under tmux's ~16 KB limit: above it codex drops its
  `-c` override first (the file and `AGENTDECK_IDENTITY_FILE` stay), and a
  block too big to carry at all (dozens of attached skills, say) is not
  injected on that spawn; agent-deck logs `identity_remote_block_too_large` /
  `identity_codex_override_too_large`;
- gemini stays env-only over SSH and in a sandbox: its folder-trust state
  there cannot be checked from the controller, and an untrusted
  `--include-directories` dialog would swallow the first message.

A session created on a remote through `remote <name> add` or the TUI dialog
is spawned by that remote's own agent-deck, so it gets the block like a local
session there: the file is written on the remote, and `$AGENTDECK_IDENTITY_FILE`
in the session is a path on that host.

A session switched to another harness (`session switch-harness`, #2237) is a
new instance in the new harness; its launch command carries the new
session's own identity block through that harness's flag, while the journaled
launch plan stays unchanged.

The block ends with a deference clause: instructions from the operator, from
project or conductor files (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`) and from
the task prompt take precedence. Nothing existing is replaced: claude gets an
appended prompt (never `--system-prompt`), codex keeps its built-in
instructions and any configured `developer_instructions`, pi appends, and
gemini loads the block next to the project's own context files. Record
fields are rendered on one line each (control characters collapsed), so the
block stays under 40 lines whatever a title contains.

### Gemini and folder trust

Gemini CLI's folder trust (on by default) asks "Do you trust the following
folders being added to this workspace?" for any `--include-directories` path
that no rule in `~/.gemini/trustedFolders.json` covers. That dialog swallows
the initial message `agent-deck launch -m` types, so agent-deck only passes
the flag when it can establish that no dialog will appear:

- folder trust is disabled (`security.folderTrust.enabled = false` in gemini's
  `settings.json`), or
- a `TRUST_FOLDER` (or covering `TRUST_PARENT`) rule exists for the identity
  root, `<data-dir>/agent-deck/runtime/identity`.

Otherwise the gemini session starts without the flag, gets only
`AGENTDECK_IDENTITY_FILE`, and the pane prints a one-line hint naming the rule
to add. To enable it once for all gemini sessions, add the identity root to
`~/.gemini/trustedFolders.json` (`GEMINI_CLI_HOME` and
`GEMINI_CLI_TRUSTED_FOLDERS_PATH` are honoured the way gemini honours them):

```json
{
  "/Users/you/.local/share/agent-deck/runtime/identity": "TRUST_FOLDER"
}
```

The directory only ever contains the identity block for that one session.

## Turning it off

```toml
# config.toml (global)
[launch]
inject_identity = false
```

```bash
# per session, persisted so restarts honour it
agent-deck add --no-identity -t "Plain" -c codex .
agent-deck launch --no-identity . -c claude -m "..."
```

The TUI New Session dialog has no row for this in this release; use the CLI
flag or the config key.

## Context levels (issue #2260)

`inject_identity = false` is an all-or-nothing switch. Context levels give
three sizes of the same block instead of just on/off:

| Level | What is written |
|-------|------------------|
| `none` | nothing — no file, no harness flag (same as `inject_identity = false` / `--no-identity`) |
| `primer` | a short block: session id, title, tool and parent only, no CLI reference or skills list |
| `full` | the block shown above (session record + CLI reference + skills + completion sentinel) — the only behaviour that existed before this issue, and the default |

Precedence is **global < group < session**: the nearest, most specific
setting wins.

```toml
# config.toml — global default
[launch]
context_level = "primer"   # none | primer | full

# per group, walks ancestor groups like other [groups."<path>"] overrides
[groups."conductor/workers"]
context_level = "full"
```

```bash
# per session, persisted; restart required (baked into the spawn command)
agent-deck session set <id> context-level primer
agent-deck session set <id> context-level ""       # clear: inherit group/global
```

The legacy `--no-identity` per-session opt-out keeps meaning `none` and
keeps winning over a positive `context_level` at any layer (backward
compatibility for an operator who set it before this issue existed). The
legacy *global* `inject_identity = false`, however, is just the global
layer's value: a group or session `context_level` overrides it, the same as
any other global < group < session precedence.

## Per-session instructions files

One or more files per session can be appended to the block, for a role, the
common rules, a brief or a house style the operator wants that session to read
at every start:

```bash
agent-deck add --instructions-file ~/notes/role.md -t Develop -c claude .
agent-deck launch . -c codex --instructions-file /abs/brief.md -m "..."
# several files, in the order they are injected: repeatable, or one list
agent-deck add --instructions-file /abs/role.md --instructions-file /abs/standard.md -t Develop -c claude .
agent-deck session set <id> instructions-file "/abs/role.md,/abs/standard.md"
agent-deck session set <id> instructions-file ""        # clear
```

- `--instructions-file` is repeatable and one occurrence may carry a comma
  separated list; `session set <id> instructions-file` takes that same list and
  replaces the whole of it (it is one field, not an append). The order is the
  order the files are injected in. A path that contains a comma cannot be
  named, and is refused with a message saying so.
- Every path must be absolute (`$VAR` and `~` are expanded when it is set),
  the same file twice is injected once, and a path containing a control
  character (a newline above all) is refused: the block puts the path on a
  line of its own, where one would inject text. The list is persisted with the session
  and shown by `session show --json` as `instructions_files`, with
  `instructions_file` the first of them; a change needs a restart, like the
  context level. `add --capabilities --json` marks the flag `repeatable`,
  which is how a client tells this build from one that takes a single file.
- The files are read at every spawn, never cached, so editing one reaches the
  session at its next start, restart, resume or fork without touching the
  field.
- Their content is appended to the block above, each in a section of its own,
  `# Session instructions (from <path>)`, so every harness in the table gets
  it through the same mechanism that carries the block: the written file for
  claude, pi, omp, gemini, opencode and copilot, the inline override for
  codex, and `AGENTDECK_IDENTITY_FILE` for the env-only tools (hermes, cursor,
  crush, custom `--cmd`), which must read the file themselves as before.
- Where the block rides inline in the launch command (codex locally, and every
  SSH or sandboxed spawn), the payload has to stay inside
  `identityInlineMaxBytes` (8 KB, what tmux's ~16 KB pane-command limit
  leaves), so the largest of three shapes that fits is sent: the files'
  content; else every file named by one line asking the session to read it;
  else the block without any instructions section, because the identity block
  itself is worth more than nothing. A demoted shape is logged as
  `instructions_files_demoted` with the shape it settled on, and the
  file-delivered copy keeps the full text either way.
- With several files the budget is spent in the given order, and once one file
  is named every later one is named too: the first file (a session's role) is
  the one that survives, and the result never depends on the sizes further down
  the list.
- The measurement is made in the unit the payload is finally sent in: the
  printf argument of an SSH or sandboxed launch, and for a local codex the
  whole `developer_instructions=` value as TOML, with a
  `developer_instructions` you configured yourself counted, because codex
  receives yours and the block as one value.
- A file over 64 KB is never inlined, and a missing or unreadable file never
  fails a spawn: the section says what could not be read and why, agent-deck
  logs `instructions_file_unreadable`, and the rest of the block is unchanged.
- Context level `none` (or `--no-identity`) means nothing is injected, the
  instructions file included: there is no block to append to, and no second
  channel is opened behind that opt-out. `session primer` states it as
  `not delivered (context level none)`.
- A fork and a cross-harness switch (`session switch-tool`, for example claude
  to codex) carry the files of the session they come from: it is the same job,
  and the new harness has to be told the same thing. The copy happens before
  the fork's start command is built, so its very first spawn has them. A
  `launch` child does not inherit them: it has a job of its own, and names its
  own files when it should have any.
- Nothing is written into the project directory and no new environment
  variable is introduced.

### Inspecting what a session actually gets

`agent-deck session primer [id]` resolves the level for a session (or the
current one, auto-detected, if `id` is omitted) and prints exactly the text
that session's harness receives — or would receive on its next
start/restart — plus which layer decided the level:

```
$ agent-deck session primer my-project
Session:       my-project (8c211446-1789219548)
Context level: primer
Source:        group:conductor/workers
Identity file: /Users/you/.local/share/agent-deck/runtime/identity/8c211446-1789219548/identity.md

# agent-deck session (primer)
You are running inside agent-deck, a terminal session manager for AI coding agents.
- session id: 8c211446-1789219548
- title: my-project
- tool: claude
- parent session id: (none: this is a root session)
Run `agent-deck session current --json` for the full record and CLI reference.
It is at $AGENTDECK_IDENTITY_FILE and is regenerated on every start/restart.
```

It also prints `Instructions:` with the session's instructions files and the
state of each (`unset`, `ok`, `unreadable: <reason>`,
`named only (inline budget)`, `not delivered (inline budget)` or
`not delivered (context level none)`); the text above is the block that spawn
really delivers, so a file the budget demotes appears there as the line naming
it rather than as its content.

`--json` gives the same fields programmatically (`context_level`, `source`,
`active`, `identity_file`, `instructions_file`, `instructions_file_state`,
`instructions_files` (one `{path, state}` per file, in order),
`text`, and `skip_reason` when the level is `none`; `delivery` is `file`, or for an SSH/sandboxed session `inline (...)`
naming where the file is written on the host where the harness runs, with
`identity_file` empty). `source` is one
of `session`, `session (--no-identity)`, `group:<path>`, `global`,
`global (inject_identity=false)`, or `default`. Fields that are unset on the
session record render as `(none)` (or `(none: this is a root session)` for
the parent) exactly as the full block does — the command never invents a
value that isn't on the record.

## Verifying

Inside any session: `echo $AGENTDECK_IDENTITY_FILE`, `cat` it, or run
`agent-deck session current --json`. From outside, the flag is visible on the
pane's start command (`tmux display-message -p '#{pane_start_command}'`), or
run `agent-deck session primer <id>` to see the resolved level and text
without attaching.
