
# Agent Build Instructions — goopg

`goopg` is a from-scratch Go reimplementation of PostgreSQL. The project
target platform is x86_64 Linux only. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md`
for the authoritative goals; pick work from `.ralph/fix_plan.md`.

## Toolchain

- Go (≥ 1.22, whatever is on PATH).
- Standard libc and a Linux kernel that supports `O_DIRECT` on the filesystem
  used for the data directory (ext4 / xfs).
- No CGo unless a specific syscall is unreachable from `golang.org/x/sys/unix`.
  Justify any introduction in a design doc.

## Repository layout

```
.
├── cmd/goopg/         # Top-level CLI entrypoint (replaces postmaster + pg_ctl + initdb)
├── internal/          # All non-public packages live here
│   ├── server/        # Listener, connection lifecycle, shutdown orchestration
│   ├── protocol/      # PostgreSQL wire protocol (v3) framing and messages
│   ├── config/        # postgresql.conf, pg_hba.conf, GUC registry
│   ├── storage/       # Buffer manager, page format, file I/O (O_DIRECT)
│   ├── wal/           # Write-ahead log writer and recovery
│   ├── mvcc/          # Snapshot manager, visibility, transaction IDs
│   ├── catalog/       # System catalogs and pg_* views
│   ├── parser/        # SQL parser/analyzer
│   ├── planner/       # Query planner
│   ├── executor/      # Query executor and physical operators
│   ├── access/        # Access methods (heap, btree)
│   └── auth/          # trust / password / md5 / scram-sha-256
├── docs/design/       # Design documents (<id>-NNNN-..., e.g. root-0001-... / 0002-0001-...) — see §9 of the spec
├── postgres/          # READ-ONLY upstream PostgreSQL source — reference only
├── .ralph/            # Ralph autonomous-loop control files (DO NOT MODIFY)
└── go.mod
```

Subdirectories under `internal/` are created on demand as their corresponding
milestones are tackled. Do not create empty stubs ahead of time.

## Build

```bash
go build ./...                       # whole module
go build -o bin/goopg ./cmd/goopg    # produce the binary explicitly
```

## Test

```bash
go test ./...                                   # full suite
go test -run <Pattern> ./internal/<pkg>         # focused
go test -race ./...                             # race detector — preferred when
                                                # touching concurrency code
go test -cover ./...                            # coverage summary

# Ralph loop state consistency guard (run before final status block)
make ralph-state-guard
```

Integration tests that need a real `psql`/`pgbench` belong under
`internal/<subsystem>/...` next to the code they exercise, gated by a build
tag (`//go:build integration`) so the default `go test ./...` stays fast.

### PostgreSQL Oracle Test Port (separate from `go test ./...`)

Ported upstream TAP tests live in `internal/testport/tap_port_test.go`.
They invoke real client tools (`psql`, `pgbench`, `pg_ctl`) and can take
several minutes. Do NOT include them in the default full-suite run.

```bash
# Run all ported PostgreSQL oracle TAP tests (slow; requires client tools)
go test -v -run TestPort_ ./internal/testport/

# Run one specific oracle test
go test -v -run TestPort_Psql001Basic ./internal/testport/

# Run all testport tests (oracle TAP + integration suites)
go test -v -tags integration ./internal/testport/
```

The current ported-test inventory and deferral status is in
`docs/test-port/postgres-oracle-port-status.csv` and
`docs/test-port/postgres-oracle-port-status.md`.

Deferred suites (`status=defer` in the CSV) must NOT be run as part of
`go test ./...` — they are either expected to have failures or depend on
infrastructure not yet available. Run them explicitly when investigating
a specific feature area:

```bash
# Regress (SQL-level, requires pg_regress-compatible runner — D-001)
# see docs/test-port/upstream-regress-coverage.md

# Isolation (multi-session scheduler — D-002)
# see docs/test-port/upstream-isolation-coverage.md

# Recovery / subscription TAP (replication infrastructure — D-003, D-004)
# see postgres/src/test/recovery/t/ and postgres/src/test/subscription/t/
```

## Lint and format

```bash
gofmt -l .          # must produce empty output
go vet ./...
```

## Reference oracle: `./postgres/`

A read-only clone of the upstream PostgreSQL source tree lives at `./postgres/`.
It is the source of truth for wire format, on-disk format, GUC defaults, error
codes, system catalog shape, and SQL semantics. GNU GLOBAL tags are
pre-generated under `./postgres/`, so:

```bash
# from inside ./postgres
global -x SymbolName            # locate definitions
global -rx SymbolName           # locate references
global -f path/to/file.c        # list symbols defined in a file
```

When porting any concept, cite the upstream file path (e.g.
`postgres/src/backend/storage/buffer/bufmgr.c`) in the relevant design doc
and/or code comment. Never modify, vendor, or import code from `./postgres/`.

Markdowned official PostgreSQL documentation is placed `postgres/official_docs_in_md/` for easy reference and linking. When citing the official docs, link to

## Design reference policy

When evaluating or creating a design, treat the PostgreSQL implementation under
`./postgres/` as the oracle reference first. Mirror upstream behavior and
semantics where feasible, then adapt the design for `goopg`'s runtime model.

While using PostgreSQL as the oracle, always account for:

- Programming-language differences between C and Go (memory management,
  ownership/lifetimes, error handling style, and standard library/runtime
  behavior).
- Execution-model differences between PostgreSQL's multi-process architecture
  and `goopg`'s multi-threaded (goroutine-based) architecture, including
  synchronization, isolation boundaries, and failure-propagation behavior.

## LSP

Use Serena as the first-choice code intelligence layer for Go work in this
repository. When Serena is connected, prefer Serena symbol tools for
definition/reference/rename/refactor flows over broad file scanning.

Serena is project-scoped via `.mcp.json` and should start from the local clone
under `serena/` using:

`uv run --directory /home/ryo/work/goopg/goopg/serena serena start-mcp-server --context=claude-code --project /home/ryo/work/goopg/goopg`

`gopls` is still required as the underlying Go language server for Serena's Go
support. If missing, install it with:

`go install golang.org/x/tools/gopls@latest`

If Go symbol operations fail:
1. Verify Serena is connected in Claude (`/mcp`) and reconnect if needed.
2. Ensure the active Serena project is `/home/ryo/work/goopg/goopg`.
3. If `gopls` was installed or settings changed, restart Serena (or restart the
  language server from the client) before retrying.

## Runtime expectations

- The server binary must run in the foreground; daemonization is not a goal.
- Operator-facing actions PostgreSQL drives via signals are implemented as
  `goopg ctl <subcommand>` (see §3.3 of the spec). The minimal set the
  process accepts directly is `SIGINT` and `SIGTERM`, which are translated
  internally to the same path as `goopg stop`.

## Loop discipline (for Ralph)

- One item per loop. Pick the topmost unchecked task in
  `.ralph/fix_plan.md` unless a dependency forces another order.
- Search before assuming something is missing. Prefer reading the spec and
  the upstream source over guessing.
- Land a design doc alongside or just before any non-trivial subsystem. This
  is a hard requirement, not optional documentation.
- For any non-trivial subsystem item, create/update the corresponding
  `docs/design/<milestone-or-spec-id>-NNNN-*.md` file and update
  `docs/design/README.md`
  in the same loop and commit.
- Do not keep bare `NNNN-*` placeholders in active tasks. Replace them with
  concrete `<id>-NNNN-*` filenames before implementation begins.
- Tests are valuable, but per `PROMPT.md` they should not exceed ~20% of a
  loop's effort. Implementation > documentation > tests when prioritising.
- Update `.ralph/fix_plan.md` at the end of every loop: tick boxes, add
  newly-discovered follow-ups, and note any tasks that turned out to be
  larger than expected.

## Completion and Deferral Discipline

- Do not mark a milestone as complete in `.ralph/fix_plan.md` until there is
  clear evidence that every milestone requirement is actually finished.
- Apply the same standard to individual tasks: only mark complete when the
  task is truly done, verified, and no required work remains.
- Do not use "deferred" or "future work" as an easy escape hatch for
  unfinished required scope.
- If a blocker prevents completion, record the blocker explicitly and keep the
  task and milestone unchecked.
- If work must be delegated to a later task or milestone, add explicit
  cross-referenced follow-up tasks so the handoff is unambiguous from both the
  source and destination entries.
- "Partially complete" is still incomplete. Never mark partial completion as
  done.

## GUC sample-file discipline

`internal/config/postgresql.conf.sample` is the operator-facing template
for every supported GUC. `goopg init` writes its bytes verbatim to
`<datadir>/postgresql.conf` (see M0108 + design doc
`docs/design/0108-0001-postgresql-conf-sample-template.md`). It is
hand-maintained, mirroring PG 18.3's section structure.

When you register a new GUC under
`internal/config/defaults.go::BuildDefaultRegistry` (or remove an existing
one), you MUST update `internal/config/postgresql.conf.sample` in the
same commit:

- Add a commented-out entry under the appropriate PG-style section,
  matching the template's existing formatting (unit / range / enum hint
  inline, restart-class annotation when `Context == ContextPostmaster` /
  `ContextSighup`, default value equal to the GUC's `BootVal` so the
  file remains a usable no-op when copied verbatim).
- For removals, delete the corresponding line and re-flow surrounding
  whitespace.
- GUC names must match PG's names exactly — operators rely on lifting
  tuned PG `postgresql.conf` files against goopg.

The unit test `TestSampleConfigCoversRegistry` in `internal/config/sample_test.go`
is the mechanical enforcement gate; it MUST pass before the commit is
opened. Letting the sample drift from the registry is a regression on
usability and on PG-operator-mental-model compatibility.

If a GUC must NOT appear in the template (internal-only, not file-settable),
mark it `FlagDisallowInFile` in its registration so the sync test
recognises the exemption.

## Key Learnings

- Go module path is `github.com/goopg/goopg` (placeholder; rename if a real
  origin is chosen later).
- Reported `server_version` is tracked in design doc `root-0001-architecture-overview.md`
  so client gating (`pgx`, JDBC, `psql`) behaves predictably.

## Vanilla PG Compatibility (ABSOLUTE)

The entire purpose is compativility with a **vanilla, unmodified PostgreSQL**.
If something doesn't work, the fix belongs in **goopg**, not in PG.

**Permitted PG interactions**:
- Adding `elog(DEBUG1, ...)` calls for diagnostic purposes (must be reverted
  after the investigation concludes).
- Reading PG source code to understand wire format, catalog layout, and
  expected invariants.
- Running `make install` to rebuild PG after adding/removing debug logging.

Absolutely forbidden:
- Changing PG function signatures, struct layouts, or logic.
- Adding `if (goopg_compat) {...}` branches or similar workarounds.
- Any change that would make PG behave differently from upstream release.

