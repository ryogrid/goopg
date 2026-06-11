
# Agent Build Instructions — goopg

`goopg` is a from-scratch Go reimplementation of PostgreSQL. The project
target platform is x86_64 Linux only. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md`
for the authoritative goals; pick work from `.ralph/fix_plan.md`.

## At Start of Session

You MUST execute the following commands at the start of every session.
- `export GOMEMLIMIT=15GiB`

## Memory-capped execution (WSL2 OOM containment) — MANDATORY

This project runs on a 32 GiB WSL2 box with a 64 GiB swap file. A runaway
goopg (e.g. a TPC-H query that materialises a huge intermediate, or an
unbounded benchmark) first thrashes swap — the whole VM goes unresponsive for
minutes — and then trips the Linux **system-wide** OOM killer, which on WSL2
routinely kills unrelated processes and terminates the entire distro. The
`GOMEMLIMIT` above is only a Go *soft* target and does not prevent this.

The fix is to run goopg inside a per-run **cgroup v2 scope** with a hard memory
cap and swap disabled, so a runaway is SIGKILLed *inside its own scope* and the
host survives. The wrapper `scripts/goopg-test-run.sh` does this via
`systemd-run --user --scope` (no root required — the `memory` controller is
delegated to the user manager on this host).

**Rule:** any command that starts a goopg server, or drives one with a heavy
workload — oracle/integration tests, TPC-H, `pgbench`, perf/benchmark runs —
MUST go through the cap. This holds whether or not you use `make`:

- **With make** (preferred — already capped):
  - `make start` — background server, capped (`goopg-server` scope).
  - `make goopg-test-server` — foreground server, capped.
  - `make stop` — graceful shutdown (works regardless of the cap).
- **Without make** — wrap the command with `scripts/goopg-test-run.sh`:

  ```bash
  # bring up a test server on the isolation port, capped + backgrounded
  GOOPG_CG_UNIT=goopg-test scripts/goopg-test-run.sh \
      ./bin/goopg start -D tmp/perf-optimize/data --listen 127.0.0.1:5533
  # stop a backgrounded scope by name:
  systemctl --user stop goopg-test.scope

  # oracle / integration tests that boot a server
  scripts/goopg-test-run.sh go test -v -tags integration ./internal/testport/...

  # benchmarks against a running server
  scripts/goopg-test-run.sh pgbench -i -s 10 -h 127.0.0.1 -p 5533 -U postgres postgres
  ```

Tunables (env vars; defaults sized for this host — see the script header):
`GOOPG_MEM_HIGH=20G` (soft cap, throttle), `GOOPG_MEM_MAX=24G` (hard cap, kill),
`GOOPG_MEM_SWAP_MAX=0` (no swap), `GOOPG_CG_UNIT` (distinct name per concurrent
run). Concurrent capped runs each need their own `GOOPG_CG_UNIT`.

Light, server-less unit tests (`go test ./internal/<pkg>/...`) do not need the
wrapper. If `systemd-run` / cgroup delegation is unavailable (e.g. CI), the
wrapper prints a warning and runs the command **uncapped** rather than failing.

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
# Regress (SQL-level) — D-001 infrastructure is now available:
scripts/pg-regress-runner.sh                   # default ~40 quick type tests
scripts/pg-regress-runner.sh --all             # all 232 upstream tests (slow)
scripts/pg-regress-runner.sh -v int4 float8    # specific tests, show diff on fail
# Reports parity % and writes diffs to tmp/regress-diffs/.
# Re-run after any SQL surface change; rising pass rate = converging PG compat.

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

## Pre-commit test gate — MANDATORY

Before you create **any** commit, you MUST run the project's unit/component
test suite, and it MUST pass cleanly:

```bash
scripts/ralph-precommit-test.sh
```

For executor/planner/codec changes, additionally run the TPC-H silent-regression
spot-check (fresh capped server + Q12/Q13 canonical row counts, ~1 min; skips
cleanly when no TPC-H data dir is loaded):

```bash
scripts/tpch-spotcheck.sh
```

For planner/executor changes, also diff EXPLAIN plans against the latest baseline
(skips when no bench server or no baseline exists):

```bash
make plan-gate
# or: RALPH_PRECOMMIT_PLAN_DIFF=1 scripts/ralph-precommit-test.sh
```

For changes to concurrency-critical packages (`internal/lock`, `internal/mvcc`,
`internal/storage`, `internal/aio`, `internal/wal` …), run the race-detector pass:

```bash
make race-gate                         # standalone (~15 min, covers all non-cluster pkgs)
# or: RALPH_PRECOMMIT_RACE=1 scripts/ralph-precommit-test.sh
```

This runs exactly the test set that CI's **"Run unit and component tests"**
step runs (`.github/workflows/test.yml`): the whole module minus the
cluster-backed packages that need a live goopg/PostgreSQL server. A green run
here is the bar every commit has to clear, so the branch never lands in a
state that CI would reject.

If the run surfaces **any** failing or flaky test, fixing it is mandatory and
takes priority over closing the current task:

- Fix it **even when the failure is unrelated to the current loop's task.**
  "Not my task" is never a reason to commit over a red or flaky suite — a
  broken shared suite blocks every subsequent loop.
- A test that passes only intermittently (flaky) counts as failing. Make it
  deterministic — do not paper over it with retries, sleeps, or by skipping
  it.
- Fold the fix into the **same commit** as the current work (or a preceding
  commit on the same branch) so the tree is never committed while red.

Never commit while this suite is red. If a failure is genuinely impossible to
resolve within the current loop, stop and record the blocker per the
**Completion and Deferral Discipline** rules below instead of committing
around it.

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

Faster than grep/global for most lookups: the `mcp__any-script__pg_*` MCP tools
query a pre-built symbol database over this tree —
`pg_search_symbols` (SQL-LIKE patterns like `heap_%`), `pg_symbol_source`,
`pg_symbol_overview`/`pg_symbol_document`, and `pg_references_to`/`pg_references_from`
for caller/callee analysis. Prefer them; fall back to `global -x` when they miss.

When porting any concept, cite the upstream file path (e.g.
`postgres/src/backend/storage/buffer/bufmgr.c`) in the relevant design doc
and/or code comment. Never modify, vendor, or import code from `./postgres/`.

Markdowned official PostgreSQL documentation is placed `postgres/official_docs_in_md/` for easy reference and linking. When citing the official docs, link to the
corresponding file under that directory (repository-relative path).

### Quick compatibility check: goopg vs vanilla PG 18.3

When you want to verify that goopg returns the same output as upstream PG for a
specific SQL snippet or file, use the oracle-diff harness instead of starting
two psql sessions manually:

```bash
# Check a SQL file against both (requires both servers running):
scripts/pg-oracle-diff.sh path/to/query.sql

# Check inline SQL:
scripts/pg-oracle-diff.sh --sql "SELECT array_agg(i) FROM generate_series(1,5) i"

# Auto-start throwaway servers, run, teardown:
scripts/pg-oracle-diff.sh --auto-start path/to/query.sql

# Run a PG regress test and see current parity:
scripts/pg-regress-runner.sh int4              # PASS/FAIL + diff
```

A `PASS` means goopg output (after normalisation) matches PG 18.3 exactly.
Any `FAIL` is a goopg compatibility bug to fix — never a reason to adjust PG.

### Parity dashboard (no live server needed)

```bash
make parity-dashboard       # writes docs/parity-dashboard.md
# Current baseline: GUC 17%, SQLSTATE 100%, pg_catalog 20%
```

Rising scores here are a lagging indicator of PG compatibility coverage.
Use them to identify which GUC or catalog gap is blocking a specific feature.

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

