# M1 — Git Workspace Engine

Status: **implemented, pending milestone review**. The architecture was conditionally
approved with narrowly scoped creation recovery and a future-compatible name policy.
No agent execution, branch deletion, history rewriting, or M2 functionality is included.
Verification evidence and current limits are reported with delivery; this document
specifies the implemented behavior rather than implying tests on every platform.

## Repository identity and shared metadata

Native Git discovers the current worktree root, private Git directory, and absolute
common directory using `rev-parse`. All existing paths are canonicalized. `.git` may
be a file; neither metadata discovery nor worktree management assumes a `.git`
directory in the checkout. Worktree registration is parsed as NUL-delimited porcelain.

The existing `<metadata-root>/.agit/agit.db` stays in place. A random repository ID
is stored in SQLite and a versioned locator. Runtime locking is scoped by the
canonical common directory; identity is independent of directory names and paths.

```text
<git-common-dir>/
  agit-control.lock                 # Stable, format-marked OS advisory lock file
  agit-control/
    format                          # Identifies the AGIT control namespace
    metadata.json                   # Repository ID and relative metadata location

<metadata-root>/.agit/
  agit.db                           # Shared authoritative AGIT records
  agit-v1-backup-<random>.db         # Consistent pre-upgrade snapshot
  workspaces/
    format
    <workspace-id>/
      owner.json                    # Durable ownership + creation operation token
      tree/                         # Native Git worktree
```

The lock is directly under the common directory, beside the control namespace.
This small layout adjustment permits locking *before* namespace initialization and
avoids a bootstrapping race. It does not put the database or workspace contents in
Git administration. Lock files and ownership/locator manifests are synced and
published without replacement through exclusive hard-link publication. Unknown
existing files/directories, symlinks, and unsupported formats are rejected.

Under the repository lock, discovery inspects `.agit` at the current root and every
registered non-bare worktree root; it never recursively searches arbitrary source
folders. With no locator, exactly one valid M0 database is adopted in place. Multiple
candidates, invalid candidates, or additional independent databases after adoption
are conflicts. Preserve all of them; no newest-database selection or automatic merge.
Candidate inspection uses a read-only SQLite connection and performs no migration.

A locator must point to that same registered metadata anchor and match the database
repository ID. A stale locator or missing database never authorizes initialization
of a replacement. `agit init` refuses any existing metadata and preserves developer
`.gitignore` contents. M1 status continues to report the calling worktree's Git state
while transactions come from the shared database. New linked worktrees receive no
local `.agit` forwarding files.

### Relocation

Relative locator, workspace, and Git-administration paths preserve AGIT identity
when a normal repository and its metadata move together. Git's own linked-worktree
pointers may need developer-directed `git worktree repair`; AGIT never runs repair
or prune automatically. Moving only the metadata anchor or an external common
directory invalidates discovery and requires an explicitly reviewed rebinding
procedure. Removing the metadata anchor is unsupported. A normal clone gets no AGIT
metadata; manually copying databases between repositories is not a supported import.
Nested independent repositories resolve independently. Bare repositories are rejected.

## Workspace model and naming

`internal/workspace.Workspace` contains immutable `ID`, `TransactionID`, `Name`,
`Branch`, `Path`, and `BaseCommit`, plus `State`, `CreatedAt`, and `UpdatedAt`.
Persistence also records the creation operation token, current operation kind,
relative administrative identity, last error, and removal timestamp. IDs/tokens
are independent random 128-bit lowercase hexadecimal values.

Names must match `[a-z][a-z0-9-]{0,47}` and cannot end with a hyphen. Reject uppercase,
Unicode, dots, separators, traversal, whitespace, and leading options; never
normalize silently. Workspace names are unique within a transaction in M1.

```text
Branch: agit/<transaction-id>/<workspace-id>-<name>
Path:   workspaces/<workspace-id>/tree   # Relative to the metadata directory
```

Validate refs with native Git, reject exact/case-folded/prefix branch collisions,
and let Git's exclusive `-b` creation enforce the final check against outside writers.
Never adopt a preexisting branch or use `-B`. The contained ID-based directory is
created exclusively and must be covered by the metadata anchor's Git ignore rules.
No user-provided workspace destination is accepted. Symlink components, non-regular
ownership files, and inconsistent paths/branches are rejected.

### Future name reuse

M1 preserves historical records and permanently reserves their names. A future
versioned migration can rebuild the workspace table without its all-history
`UNIQUE(transaction_id, name)` constraint and add a partial unique index covering
non-removed records. IDs, branches, paths, foreign keys, and tombstones remain
unchanged. That milestone must add ID-based historical lookup and specify how
name-based commands select the current record; it must not delete old rows or reuse
old IDs. This is a compatible future direction, not implemented behavior.

## Transaction selection and committed bases

Every workspace command supports `--tx <full-transaction-id>`. Without it, exactly
one active transaction must exist. Zero means no eligible transaction; multiple
means an explicit ambiguity error listing IDs. Never choose the newest transaction.
An explicit ID must exist in this shared database. Creation requires active state;
explicit list/status/remove can address terminal transactions for cleanup.

Every workspace starts at the transaction's stored commit, not the caller's newer
HEAD or uncommitted changes. Verify a complete commit object ID through Git before
persisting creation intent. M0 unborn transactions remain inspectable but cannot
create workspaces: commit first, then begin a new transaction. Temporary acceptance
fixtures must therefore have an initial commit before `agit tx begin`.

## Lifecycle and durable intent

Persist lowercase states: `creating`, `ready`, `error`, `removing`, `removed`.
Legal transitions are explicit:

| From | To | Gate |
| --- | --- | --- |
| New | creating | Validated inputs and committed intent. |
| creating | ready | Ownership, native registration, base, checkout, and persistence verified. |
| creating | error | Incomplete, failed, or contradictory creation evidence. |
| ready | error | Recorded resources cannot be verified. |
| ready/error | removing | Explicit removal request and full safety preflight. |
| removing | removed | Working directory, registration, and private administrative directory absent. |
| removing | error | Removal failed or evidence contradicts intent. |
| error | ready | Ownership re-established with the appropriate evidence gates. |
| error | removed | Prior remove intent and complete observed absence. |

Removed is terminal. Updating an ERROR diagnostic without changing state is allowed.
SQLite updates compare expected state and immutable identity; administrative identity
cannot be replaced once recorded. There is no arbitrary domain state setter.

### Creation sequence

1. Lock, resolve shared metadata and transaction, validate name/commit/ref/destination.
2. Commit CREATING with its operation token before any Git mutation.
3. Exclusively create and sync the ID container and token-bearing outer manifest.
4. Execute native `git worktree add -b <branch> <path> <base>` with no force flags.
5. Verify common directory, exact registered path/branch/base, native gitfile and
   administrative backpointer, and completed clean checkout. Publish the matching
   administrative ownership manifest and commit READY.

Git and SQLite are not atomic together. Errors preserve branches, directories, and
intent. If an error cannot be persisted, the earlier CREATING record remains the
recovery anchor. Never perform destructive creation rollback.

### Removal sequence

Resolve under the lock and validate the outer/admin manifests, repository ID,
operation token, stored private Git identity, registered path/branch, and containment.
Reject the invoking, metadata-anchor, main, locked, or prunable worktree.

Inspect tracked/staged changes, untracked **and ignored** files, hidden index flags
(`assume-unchanged`/`skip-worktree`), empty developer-created directories, active Git
operations, initialized submodules, and nested repositories. Any inspection error
blocks removal. Git's clean-worktree test alone can discard ignored data.

Commit REMOVING, repeat preflight immediately before native `git worktree remove`
without force, then verify complete absence and commit REMOVED. Retain the branch
and the outer container/manifest as a tombstone. There is no recursive cleanup.
A repeated remove of a recorded removed workspace is idempotent.

The repository lock cannot freeze external Git or editor processes. Stop writers
before removing a workspace. The preflight/Git checks narrow but cannot eliminate
the check-to-removal race, particularly for ignored files.

## Recovery and reconciliation

Workspace list/status and subsequent operations reconcile records under the same
lock. They never automatically create a worktree, remove one, delete a branch,
prune unknown registrations, or repair Git pointers. They may finalize metadata
and recover an ownership marker using the narrowly defined evidence below.

| Observed situation | Behavior |
| --- | --- |
| Intent without complete Git resources | ERROR; preserve record/name and any branch/container. |
| Branch alone | ERROR; branch naming never establishes ownership. |
| Git creation completed, READY SQL update failed | Verify ownership and original base, then finalize READY. |
| Missing admin manifest after interrupted creation | Recover only with matching persisted token/outer manifest, repository ID, native registration, exact path/branch/base, native gitfile/backpointer, and clean completed checkout. Publish a marker exclusively, then READY. |
| Missing/mismatched outer manifest, contradictory admin manifest, dirty incomplete checkout, or changed pending-creation base | ERROR; preserve resources and require manual intervention. Never replace contradictory evidence. |
| READY directory missing or registration stale | ERROR; preserve branch and registration. Never pretend removal completed. |
| REMOVING but owned worktree still present | Report pending state; only another explicit remove request may resume after full preflight. |
| Removal complete, final SQL update failed | Verify durable remove intent, tombstone, and path/registration/admin absence; finalize REMOVED. |
| Missing path with remaining registration/admin directory | ERROR; manual native Git recovery required. |
| Git failure/cancellation or SQLite finalization failure | Report unknown/partial outcome; reconcile from intent and resources, never from exit status alone. |

Once READY, commits on the owned branch may advance normally; they do not change
its original base or identity. Ownership manifests prevent accidental confusion,
not attacks by a process already able to modify the same user's database and files.
There is no generic repair framework or recovery CLI in M1.

## SQLite migrations and compatibility

Migration 1 remains unchanged. Low-level M0 storage initialization/open retains its
v1 behavior; shared project sessions explicitly apply migration 2 during adoption.
New CLI initialization creates v1 and immediately adopts it. This preserves the
existing M0 tests and data while avoiding an unlocked low-level auto-upgrade.

Before migration 2, use parameterized `VACUUM INTO` to create a consistent uniquely
named, synced private v1 snapshot, including committed WAL data. Fail adoption if
backup fails. Migration 2 runs atomically and adds:

- Singleton repository identity with a generated ID.
- Workspace table and lifecycle/operation/ownership timestamps and diagnostics.
- Transaction foreign key with `ON DELETE RESTRICT`.
- Unique workspace IDs, branch/path/token, and `(transaction_id, name)`.
- Required-field, name, ID, committed-base-length, and lifecycle checks.

Advance `user_version` to 2 in that migration transaction. Interrupted locator
publication reuses the already committed repository ID; it never regenerates it.
Enable foreign keys, busy timeout, and FULL synchronization on each connection.
Keep one connection per store and parameterized statements. Use the existing
rollback journal default; no distributed persistence infrastructure is introduced.

Version 2 does not rewrite any transaction record. Old M0 binaries reject v2 as a
future schema; concurrent mixed-version writers and downgrading are unsupported.
Multiple M0 stores are not automatically merged. Future schema versions are rejected.

## Concurrency, platform, and execution boundaries

All CLI metadata operations and reconciliation use an exclusive OS advisory lock
on the stable common-directory lock inode. Wait up to five seconds, respecting
context cancellation. Process exit releases it; never unlink a lock or guess stale
PID timeouts. Acquire repository lock before short SQLite transactions; keep it
through Git mutation and final persistence, with no SQL transaction spanning Git.
Git ref checks and SQLite constraints remain necessary for external writers.

The existing `golang.org/x/sys` dependency is now used directly for macOS/Linux
no-follow file opening and file locks. No new framework or runtime service is added.
Windows and network-filesystem guarantees are unsupported. Capability failures in
native Git fail clearly rather than falling back to parsing ad hoc `.git` layouts.

Git commands use argument slices, context cancellation, a two-minute subprocess
limit, raw-byte APIs for machine output, and typed errors retaining exit status,
stderr, and causes. Git environment overrides are sanitized. Managed commands disable
hooks, fsmonitor, recursive submodule behavior, and automatic maintenance through
per-command options without editing developer Git configuration. External checkout
filters are rejected. This remains trusted local developer infrastructure, not a
sandbox for malicious repository configuration or same-user adversaries.

Owned file reads reject final-component symlinks; directories and immutable
manifests are revalidated around mutations. This prevents ordinary ownership/path
confusion, but cannot eliminate every concurrent directory-replacement race in an
external native Git process. Repository-wide advisory locks coordinate AGIT only.

## Package boundaries and verification coverage

CLI commands are thin presenters. `internal/project` owns shared discovery,
initialization, adoption, and lock lifetime. `internal/git` owns native operations.
`internal/workspace` owns the model, transaction selection, lifecycle, ownership,
and reconciliation. `internal/storage` owns schema and parameterized persistence.
`internal/safeio` provides small exclusive-publication/no-follow primitives, not a
generic filesystem or recovery framework. The workspace persistence interface exists
for deterministic SQLite failure injection; Git errors are tested at subprocess level.

Tests preserve M0 cases and add temporary repositories/databases for independent
worktrees, duplicate/invalid names, branch collisions, shared metadata, restarts,
legacy adoption/conflicts, migration rollback/snapshots/foreign keys, safe and rejected
removal, ownership and path mismatches, Git failures before/after mutation, crash
windows, process concurrency, cancellation, and process-exit lock release.
Acceptance uses the built binary in a committed temporary repository; no destructive
integration tests target the AGIT development checkout.

## Remaining limitations and deviations

- Lock file is beside rather than inside the marked control namespace to serialize
  initial namespace creation. Database placement and scope are unchanged.
- Migration 2 is explicitly applied by locked project sessions instead of every
  low-level storage open; original v1 initialization tests remain intact.
- Approved creation recovery can reconstruct a missing administrative marker from
  complete evidence; incomplete evidence still requires manual recovery.
- Empty directories and hidden index flags block removal in addition to the originally
  listed dirty checks, protecting changes Git status may omit.
- Metadata remains anchored to one worktree. Moving external layouts, missing anchors,
  conflicting legacy stores, partial control-namespace creation, and ambiguous Git
  failures need manual intervention; no automatic import/rebind/prune is provided.
- Names, backups, and tombstones accumulate intentionally in M1.
- No guarantee is claimed for external concurrent writers, hostile same-user processes,
  Windows, network filesystems, checkout filters, or initialized submodule removal.

## Native Git references

- [git-rev-parse](https://git-scm.com/docs/git-rev-parse)
- [git-worktree](https://git-scm.com/docs/git-worktree)
- [git-status](https://git-scm.com/docs/git-status)

These define Git contracts; the ownership, lifecycle, and metadata policies are AGIT's.

## M1 verification record — 2026-10-08

Executed on macOS with Go 1.26.5 and Git 2.54.0:

- `gofmt -w cmd internal`: completed; final formatting inspection found no output.
- `go test -json -count=1 ./...`: 65 top-level tests and 25 subtests passed,
  zero failures; two subprocess helpers skipped outside their child invocation.
- `go test -race ./...`: passed for all tested packages.
- `go vet ./...`, `go build ./...`, and `go build -o agit ./cmd/agit`: passed.
- `GOOS=linux GOARCH=amd64 go build ./...`: passed. Linux runtime tests were not
  executed on this macOS host; cross-compilation is not runtime acceptance.
- `git diff --check`: passed.

The built CLI passed init, status, transaction begin/status, creation of backend
and frontend, workspace list/status, and shared transaction reads from both linked
worktrees in a temporary committed repository. Separate command invocations proved
process-restart persistence. Native Git registration and independent branch names
were checked, and SQLite schema/records were inspected read-only. Dirty tracked and
ignored-file removal attempts exited 1 with their contents preserved. Both clean
removals exited 0, retained their branches and historical records, and the original
checkout's files, HEAD, branch, and Git status remained unchanged.

Failure tests simulate interrupted creation/removal and final SQLite failure; Git
subprocess doubles also fail before and after real native Git mutation. A killed
lock-holder subprocess proves process-exit lock release. No claim is made about
power-loss testing, untested operating systems, or protection from external writers.
No developer-repository worktrees were created or removed. No commit or push occurred.

### M1 file inventory

- `internal/cli/workspace.go`: new command presentation; root and transaction handlers
  use shared sessions/register workspace commands; CLI tests extended.
- `internal/git/repository.go`, `worktree.go`: common-directory discovery, typed/raw
  command execution, worktree operations, ref and removal safety; Git tests extended.
- `internal/project/init.go`, `discovery.go`, `lock_unix.go`, `lock_other.go`,
  `discovery_test.go`: shared initialization/adoption, locator, lock lifecycle, tests.
- `internal/storage/sqlite.go`, `workspaces.go`, `workspaces_test.go`: connection
  pragmas, explicit migration 2, snapshot, workspace persistence and constraints.
- `internal/workspace/model.go`, `service.go`, `model_test.go`, `service_test.go`:
  identity/lifecycle, selection, ownership, recovery, safety and failure tests.
- `internal/safeio/files.go`, `open_unix.go`, `open_other.go`, `files_test.go`:
  exclusive durable publication, no-follow reads, platform guard, tests.
- `go.mod`: existing x/sys module promoted to a direct dependency.
- `README.md`, `docs/architecture/workspaces.md`: final usage, compatibility,
  recovery, platform limitations, risks, and verification evidence.

Pre-existing uncommitted M0 work and developer changes are retained; this inventory
describes M1's substantive changes rather than treating the whole dirty tree as new.
