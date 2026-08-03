# Codex Session Guard

[![CI](https://github.com/kabxx/codex-session-guard/actions/workflows/ci.yml/badge.svg)](https://github.com/kabxx/codex-session-guard/actions/workflows/ci.yml)

Cross-platform launcher and crash recovery for Codex sessions. `csg run` supervises a Codex-compatible launcher, records its Session ID, and keeps a recoverable record when the terminal, process tree, or computer stops unexpectedly.

CSG is opt-in. Running `codex` or `spine-codex` directly does not install a wrapper, create a record, or change how that command resolves.

## Features

- Explicit monitoring through `csg run` only
- Live status listing plus Session ID based recovery and deletion
- Windows Job Objects for CMD/BAT and descendant process supervision
- Linux and macOS process-group supervision with one foreground supervisor per session
- Crash-safe run records with PID and process-start identity checks
- Launcher-agnostic recovery: the original launcher is used again
- Transactional install, upgrade, rollback, and ownership-checked uninstall
- Windows, Linux, and macOS CI coverage

## Quick Start

### Windows

```powershell
pwsh ./scripts/install.ps1
csg doctor
```

### Linux and macOS

```sh
sh ./scripts/install.sh
csg doctor
```

The installers place `csg` and `codex-session-guard` in `~/.local/bin` (or `%USERPROFILE%\.local\bin` on Windows). They do not replace the user's `codex` command.

## Usage

Start a monitored session with the launcher you normally use:

```powershell
csg run spine-codex.cmd
csg run imba_codex --profile work
csg run .\tools\foo-codex.cmd --model fast
```

On Linux or macOS:

```sh
csg run spine-codex --profile work
csg run ./tools/foo-codex.sh
```

Inspect every session currently tracked by CSG:

```text
csg list
```

The list reports four states:

| Status | Meaning |
| --- | --- |
| `RUNNING` | The monitored session is active. A `pending (waiting for first turn)` Session value means the launcher has not supplied a Session ID yet. A `prebound; waiting for Hook confirmation` value means CSG already knows an explicit resume UUID and is waiting for the Hook to confirm it. |
| `UNKNOWN` | Process liveness could not be verified safely; the session is not treated as recoverable. |
| `CRASHED` | The monitored process tree stopped unexpectedly and the session can be resumed. |

Sessions that exit normally or through a user interrupt are removed instead of being retained as history. To resume a crashed session:

```text
csg resume <SESSION_ID>
```

To discard a recovery record:

```text
csg delete <SESSION_ID>
```

`resume` and `delete` accept a complete Session ID only. They do not accept list positions or `--all`. Deleting a record removes CSG metadata only; Codex transcripts and session history are untouched.

Other commands:

```text
csg doctor
csg version
csg help
```

The first launcher's extra arguments are used only for that launch. They are not stored or replayed during recovery, which avoids persisting prompts or secrets. Recovery returns to the recorded working directory and invokes the original launcher with `resume <SESSION_ID>`.

## How It Works

1. `csg run` writes a run record before starting the requested launcher and passes a random run ID through the environment.
2. The Codex `SessionStart` Hook binds the exact Session ID, transcript path, working directory, and source to that record.
3. The supervisor waits for the complete process tree, including a script that exits before its child Codex process.
4. A normal exit or user interrupt removes the record. An abrupt stop leaves it on disk for the next `csg list`.
5. `csg list` checks PID plus process-start identity and reports active and crashed sessions without mistaking a reused PID for the old process.
6. `csg resume` atomically claims the record, pre-binds the known UUID before starting the original launcher, and lets the new Hook confirm it and enrich the record.

Session records are written atomically before the target starts. This lets a reboot or power interruption leave a recoverable record without requiring a background service. Explicit `resume <SESSION_ID>` arguments are pre-bound before launch, so a resumed session does not need a first user turn before it can be recovered again. New sessions and name-based resume still wait for the SessionStart Hook to provide the resolved UUID.

## Scope and Limits

The target launcher must preserve the CSG run ID environment variable, pass through the Hook-related arguments, and support `resume <SESSION_ID>`. CSG recognizes only a valid UUID after the structured `resume` command for pre-binding; it never treats a session name as an ID. CSG does not parse the internals of CMD, BAT, or shell scripts.

Windows CMD/BAT launchers cannot receive arguments containing newlines through the Windows command-line syntax. Use a native executable when multiline arguments are required.

Processes deliberately moved to an external service, scheduled task, or escaped process group are outside the supervision boundary. Records are platform-specific and are not automatically executable after being copied to another operating system.

On Unix, if the CSG supervisor alone is forcibly killed while the target process group remains healthy, the group may continue running because no separate daemon is retained to kill it. Terminal close and computer failure remain covered by the process group and durable run record; Windows Job Object supervision still terminates the tree when the supervisor disappears.

## State and Hooks

State directories:

- Windows: `%LOCALAPPDATA%\CodexSessionGuard`
- macOS: `~/Library/Application Support/CodexSessionGuard`
- Linux: `$XDG_STATE_HOME/codex-session-guard`, or `~/.local/state/codex-session-guard`

The installer updates only the CSG-managed `SessionStart` and `SessionEnd` Hook entries and their trust records. Existing user hooks, Codex settings, backups, and PATH entries are preserved. Upgrading from an older CSG version removes legacy `codex` wrappers only when the recorded installation hash proves CSG ownership.

## Development

Prerequisite: Go 1.22 or newer.

Run the unit tests and static checks from the repository root:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Build and run the isolated integration suite:

```powershell
pwsh ./scripts/build.ps1
pwsh ./scripts/test-integration.ps1
```

```sh
sh ./scripts/build.sh
sh ./scripts/test-integration.sh
```

The GitHub Actions matrix runs native tests on Windows, Ubuntu, and macOS. Build artifacts are written to `dist/` and are intentionally ignored by Git.

## Repository Layout

```text
cmd/csg/       Go application, platform backends, and tests
scripts/       Build, install, uninstall, and integration entry points
testdata/      Small launcher fixtures used by integration tests
.github/       Continuous integration workflow
```

## Uninstall

Windows:

```powershell
pwsh ./scripts/uninstall.ps1
```

Linux and macOS:

```sh
sh ./scripts/uninstall.sh
```

Uninstall removes only CSG-owned binaries, hooks, and trust records. It preserves the user's `codex` command, PATH entries, recovery records, and installation backups.
