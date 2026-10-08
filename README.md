# AGIT

The agent-native development platform.

**Build with any agent. Integrate with confidence.**

AGIT is a Git-compatible platform for coordinating autonomous software development.
M0 provides repository initialization and transactions. M1 adds isolated transaction
workspaces using native Git worktrees. No AI agent execution is included yet.

## Installation

Requirements: Go 1.26.5, native Git with absolute common-directory discovery and
NUL-delimited worktree porcelain support, and a macOS/Linux local filesystem.

```sh
go build -o agit ./cmd/agit
go install ./cmd/agit
```

For installed usage, add `$(go env GOPATH)/bin` to `PATH` (or your configured `GOBIN`).
Embed a version when building a release:

```sh
go build -ldflags "-X main.version=0.1.0" -o agit ./cmd/agit
./agit --version
```

## Transactions and workspaces

Run from an existing non-bare Git repository with an initial commit:

```sh
agit init
agit status
agit tx begin "Implement authentication"
agit tx status
agit tx workspace create backend
agit tx workspace create frontend
agit tx workspace list
agit tx workspace status backend
agit tx workspace remove backend
```

Use `./agit` when running the locally built binary. Every workspace command supports
`--tx <full-transaction-id>`. If exactly one active transaction exists, selection is
automatic. Multiple active transactions require `--tx`; AGIT never chooses the newest.
Explicit list/status/remove can inspect or clean up a terminal transaction.

Names contain 1–48 lowercase ASCII letters, digits, and hyphens, start with a letter,
and cannot end in a hyphen. Names remain reserved after removal in M1; workspace IDs
and historical records are retained. Each workspace has its own branch:
`agit/<transaction-id>/<workspace-id>-<name>`.

Creation starts from the transaction's recorded commit. Commit before beginning a
workspace-capable transaction. M0 transactions without commits remain valid, but
cannot create workspaces; commit and begin a new transaction. Dirty source checkouts
are preserved and their uncommitted changes are not copied into workspaces.

The printed workspace path is an isolated working directory under the metadata
anchor's `.agit/workspaces/<id>/tree`. Commands work from nested directories and
linked worktrees; they share the same repository transaction database.

## Metadata and migration

`agit init` creates `.agit/agit.db`, preserves existing `.gitignore` contents, and adds
an ignore rule when needed. It refuses existing or conflicting metadata. M1 retains
an existing M0 database at its original path and adopts it in place through schema
version 2. Adoption creates a consistent private `agit-v1-backup-*.db` snapshot first.
Transaction records are preserved; no database is silently replaced or merged.

Git's common directory contains `agit-control.lock` and a marked `agit-control/`
namespace with a relative locator and repository ID. All worktrees use that locator;
linked worktrees need no independent `.agit` directory. Multiple legacy databases,
stale locators, invalid ownership, or missing metadata cause explicit errors.

M1 CLI metadata commands, including status, can adopt/migrate a legacy database and
therefore require writable metadata. Old M0 binaries cannot open schema v2; mixed
M0/M1 writers and downgrade are unsupported. Keep migration backups for recovery.

## Safe removal and recovery

Removal never uses force and never deletes the workspace branch. It rejects tracked,
staged, untracked, and ignored changes; hidden index flags; empty developer-created
directories; initialized submodules; nested repositories; active Git operations;
and locked or mismatched worktrees. Run removal from another repository worktree and
stop editors/builds before removing. Git status alone can miss data that Git removes.

Lifecycle states are `creating`, `ready`, `error`, `removing`, and `removed`. Operation
intent is committed before Git mutation. Workspace list/status reconcile interrupted
operations: complete creation evidence may finalize `ready` and recover a missing
administrative ownership marker; completed removal may finalize `removed`. Pending
removal with a directory still present needs an explicit removal retry.

Incomplete or contradictory evidence preserves resources and reports diagnostics.
No automatic branch deletion, worktree pruning, Git repair, stash/reset, or recursive
cleanup occurs. Some interrupted operations require manual inspection. Do not delete
`.agit` or edit SQLite to conceal a failure; retained branches remain ordinary Git
resources recoverable without AGIT.

## Development and verification

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./...
git diff --check
go build -o agit ./cmd/agit
```

Tests use temporary Git repositories and SQLite databases, including independent
processes for concurrency and injected persistence/Git failures. Destructive tests
never target the developer checkout. See [workspace architecture](docs/architecture/workspaces.md)
for package boundaries, migration details, ownership gates, and recovery behavior.

## Platform and current limitations

M1 targets macOS/Linux local filesystems; Windows and network-filesystem guarantees
are unsupported. External checkout filters are rejected. Hooks and fsmonitor are
disabled for managed Git operations. This is trusted local infrastructure, not a
sandbox against malicious repository configuration or same-user processes.

The repository lock coordinates AGIT processes, not outside Git commands or editors;
a removal race remains possible with concurrent external writers. Moving a normal
repository preserves relative metadata paths, but linked worktree pointers may need
manual `git worktree repair`. Moving external Git/metadata layouts or deleting the
metadata anchor requires a separately reviewed recovery procedure.

M1 provides no general recovery CLI, branch deletion, transaction completion command,
agent execution, integration pipeline, frontend, cloud service, or LLM dependency.
Names, tombstones, and backups are intentionally retained. Future name reuse will
require a migration and explicit historical lookup rules.
