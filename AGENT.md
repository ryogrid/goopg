
# Agent Build Instructions — goopg

`goopg` is a from-scratch Go reimplementation of PostgreSQL. The project
target platform is x86_64 Linux only. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md`
for the authoritative goals; pick work from `.ralph/fix_plan.md`.

## At Start of Session

You MUST execute the following commands at the start of every session.
- `export GOMEMLIMIT=15GiB` (heavy queries may need lower limits — TPC-H Q21
  completed at `GOMEMLIMIT=12GiB + GOGC=100`; see CLAUDE.md for details.)

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

### Long-running tests: poll, don't sit waiting

If you lack a mechanism or tool that automatically notifies you when a
`monitor` command or a background job completes, do not end your turn "waiting"
for a long test or benchmark to finish — in a headless loop that turn is
discarded and the work is re-run. Detect completion yourself by wrapping the
wait in a shell check loop that polls at a suitable interval in seconds:

```bash
# e.g. poll until the gate's log carries the done marker (or the PID is gone)
until grep -q "done-marker" gate.log 2>/dev/null || ! kill -0 "$PID" 2>/dev/null; do
  sleep 5
done
```

Choose the interval to match the job's expected duration (~5s for a minutes-long
gate, ~30s for a multi-hour sweep): a too-tight loop burns CPU for nothing, and
a loop that never reaches its termination condition wastes the whole turn.

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

The current ported-test inventory and deferral status is in the consolidated
authority CSV `docs/test-port/postgres-oracle-target-inventory.csv` (see
`docs/test-port/README.md` for the schema and promotion workflow).

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

## Parser code — READ THE PLAYBOOK FIRST

SQL parsing is a goyacc-generated LALR parser whose grammar lives in
`grammar/*.y`; everything (parser, lexer, AST, the retained compat scanners)
is in the single package `internal/parser`. The migration from the
hand-written recursive-descent parser is COMPLETE — there is no
`internal/sqlparser` any more, and no routing hook to wire.

**Before touching `grammar/*.y`, `internal/parser/`, or parser routing —
and BEFORE changing parser behaviour to fix a regress case or to match
PostgreSQL — read
`docs/design/not_ralph/06-goyacc-parser-playbook.md`, in particular §12.**
If you have already read it in this session you do not need to re-read it,
but do not skip it on the assumption that a change is "just" a grammar tweak.

§12 is the reason this instruction is emphatic. This grammar is a port of
`postgres/src/backend/parser/gram.y`, not a copy, and it carries deliberate
goopg-specific accommodations that LOOK LIKE BUGS:

- synthetic terminals the real scanner has no counterpart for (`TYPEDLIT`,
  `CHECKBODY`, the `*_LA` family) — grep `gram.y` for them and you find
  nothing, which is expected;
- `$<p>N`, this grammar's stand-in for yacc's `@n`, and the specific
  conditions under which `lastConsumedPos()` silently returns the WRONG
  token;
- four different position conventions that interact — node positions are the
  wire `ErrorResponse.Position` and psql's caret, and are NOT covered by the
  golden corpus, so a regression there is not caught automatically;
- ~1.7% of statement classes that deliberately stay on hand-written token
  scanners, where writing a grammar rule would make goopg STRICTER than it
  ships and start rejecting working input;
- an inventory of known, intentional divergences from `gram.y` in both
  directions, each with its upstream citation.

Then:

1. `docs/design/not_ralph/TODO.md` — routed classes, conflict pin, known
   diffs, deferred slices, and the full migration history including why each
   divergence exists
2. `docs/design/not_ralph/02-grammar-porting-guide.md` and
   `03-strangler-migration.md` — background and strategy

Two rules that catch most first-time mistakes:

- Build with **`make gen-parser`**, never `go build` alone — a bare build
  happily compiles a stale generated parser.
- The oracle is `internal/parser/testdata/parity_goldens.txt`. Regenerate with
  `GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/` and **read the diff** —
  it is the review artifact for your change, not a formality.

## Lint and format

```bash
gofmt -l .          # must produce empty output
go vet ./...
```

## Pre-commit test gate — MANDATORY (now machine-enforced)

**This gate is now enforced by a git hook, not just by discipline.** Run once
per clone to activate it:

```bash
make install-hooks      # sets core.hooksPath=.githooks
```

After that, **every `git commit` automatically runs the CI-parity pgbench smoke**
(`.githooks/pre-commit` → `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`:
build → init → pgbench standard / `-N` / `-S`, ~2-3 min) and the commit is
**rejected** if it fails. This closes the historical blind spot where the loop
ran only targeted `go test` and never the pgbench workload, so the pgbench TPC-B
concurrency-regression class reached CI undetected. **Do NOT bypass with
`git commit --no-verify`** — that re-opens exactly that blind spot.

The hook runs the **pgbench smoke only**. So you do NOT run pgbench by hand —
that would run it twice (once manually, once in the hook). Your manual,
mandatory pre-commit job is the **unit/component suite**, which the hook does
not run; it MUST pass cleanly before you commit:

```bash
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh   # unit/component suite only
```

Fast-gate defaults (mechanisms landed 2026-07-17; design:
`ci/design/test-gate-speedups/`):

- Throwaway test clusters run FAST by default: the smoke gate inits with
  `--no-sync`, and `testutil/cluster` inits from a per-process cached
  template (sysid re-randomized per clone) with `--no-sync`, then runs the
  server with `fsync = off`. Durability-asserting tests opt OUT with
  `SyncInit: true` + `SyncRuntime: true` (allowlist:
  ci/design/test-gate-speedups/02 §4) — write NEW crash-recovery /
  WAL-replay / replication / restart-durability tests durable-by-default
  (`newDurableCluster` in testport).
- NEVER pass `-count=1` to a gate's `go test` — it defeats the test-result
  cache and turns a ~5 min warm `units` run into a ~40 min cold one.
  `-count=1` is only for one-off validation runs (flake screens, probes).
  A cached PASS is a real PASS (scope and limits:
  ci/design/test-gate-speedups/05 §1 rule 2).

**Headless loop note:** run every gate above in the FOREGROUND (Bash timeouts
are raised to 15 min default / 60 min max in loop sessions). Never start a gate
with `run_in_background` and end your turn "waiting for the notification" — in
headless `-p` mode the task is killed at turn end and the result is lost, so the
next loop re-runs it. See "Headless Execution Reality" in `.ralph/PROMPT.md`.

Division of labour: `units` (you, once per change) + the hook's `smoke` (every
commit) = full CI parity, each part run once. Run the `full` scope
(`scripts/ralph-precommit-test.sh`, unit suite + pgbench) only when you want to
exercise pgbench locally before committing — e.g. a concurrency / executor /
storage change; the hook's `smoke` then re-runs pgbench as harmless
defense-in-depth.

For executor/planner/codec changes, additionally run the TPC-H silent-regression
spot-check (fresh capped server + Q12/Q13 canonical row counts, ~1 min; exit 0
SKIPPED only when no TPC-H data dir exists; exit 3 SKIP-BLOCKED = data missing = a
failed gate):

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
  `.ralph/fix_plan.md` unless **the `## Current Priority` banner** or a
  dependency forces another order. The banner wins over topmost placement, and
  it also outranks `.ralph/working_set.md`'s "NEXT LOOP" note, which carries
  state, not priority.
- **Priority comes from the banner.** Tasks in M0137–M0145 and `P0-` tasks
  follow §"Plan-parity harness" below — read it before selecting one.
  **M-NIGHTLY filing is unconditional:** every loop reads
  `ci/logs/action-items.md` and files each new `## AI-` subject, but M-NIGHTLY
  items are selected only when the banner says so, or when they break the build
  or a gate the banner's work depends on.
- Search before assuming something is missing. Prefer reading the spec and
  the upstream source over guessing.
- Land a design doc alongside or just before any non-trivial subsystem. This
  is a hard requirement, not optional documentation. **Overridden for
  M0137–M0145 only** — see §"Plan-parity harness" below, where the design doc is
  written when the task is *selected*.
- For any non-trivial subsystem item, create/update the corresponding
  `docs/design/<milestone-or-spec-id>-NNNN-*.md` file and update
  `docs/design/README.md`
  in the same loop and commit. (This same-loop indexing requirement is **not**
  relaxed for M0137–M0145.)
- Do not keep bare `NNNN-*` placeholders in active tasks. Replace them with
  concrete `<id>-NNNN-*` filenames before implementation begins. **Overridden
  for M0137–M0145 only**: the filename is reserved at task selection, not at
  milestone filing.
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

<!-- PLAN-PARITY-HARNESS:BEGIN -->
## Plan-parity harness (M0137–M0145) — binding

Sections R and G7 apply to **every** Ralph loop task. The rest applies to
tasks in M0137–M0145 and `P0-` tasks. Where this section disagrees with other
sections of this file, it wins for those tasks (including over
`docs/milestones/README.md` step 2 and "reserve a design-doc filename before
coding"). Rationale and history: `docs/design/0100-0149/plan-parity-harness-background.md`
(non-binding). **The loop does not edit this section** (rule R2).

### Goal
Every executable TPC-H (22) and TPC-DS (99) query gets **the same plan as PG
18.3** through the same statistics, costing and planning logic — never by
forcing shapes. A slower plan that matches is not a regression here (time is
reported, not judged). A wrong row count always is. PG's estimates —
including its errors — are reproduced by porting PG's code (owner decision
2026-09-14), never by tuning toward them. Do not write a success test of the
form "match count rises"; progress is category movement.

Score = `scripts/pg-plan-parity-diff.py` output: `MATCH` count plus the
`CATEGORIES:` and `CATEGORIES-EXCL-MATCH:` lines. **Noise band: ±3 per corpus**
(PG-side capture variance on a byte-identical goopg capture). **TPC-H parity is
measured in parallel mode** (owner decision 2026-09-20): both engines with
`max_parallel_workers_per_gather` enabled — `estimate-audit -plan-only
-serial=false`. Floor: TPC-H parallel match ≥ 3 (last measured P0-E7,
2026-09-18; re-pinned by M0144-0001's capture), TPC-DS SF0.25 match ≥ 2 (Q9,
Q41 — the floor is SF0.25 only; at SF1 the corpus reads 1/99, see P0-H12).
TPC-DS captures already pin `max_parallel_workers_per_gather=4`, unchanged.
Serial-mode TPC-H (match 8/22 at `27d4ae001`) stays a diagnostic capture, not
the floor. Losing a current match is a regression.

### R — Hard prohibitions (no exceptions, no "dry run")
- **R1 Shared clusters.** Reference clusters `:65432` (PG TPC-H), `:65433`
  (goopg TPC-H), `:65438` (PG TPC-DS) are **read-only**: only `SELECT`,
  `EXPLAIN`, `pg_basebackup`. Never DDL, DML, `ANALYZE`, `VACUUM`,
  `pg_terminate_backend`, stop/restart in any mode, `--reset`, reload, or
  rebuilding the binary they serve — not inside `BEGIN … ROLLBACK`, not to fix
  something. Anything that writes uses a private clone on a `55xx` port.
  The gate-owned cluster `:65437` is started/stopped only by
  `scripts/tpcds-sf025-regression.sh`. A reference cluster that is merely
  **down** may be started with `scripts/ref-clusters-ensure.sh` (never
  otherwise). A cluster that is **broken** (data missing, HOLD file present):
  mark the task `[!]` with an escalation block and select elsewhere.
  Anything with a sibling `*.HOLD` file (`bench/tpch/runtime_goopg/data.HOLD`,
  `…/preloss-clone-20260915.HOLD`) is evidence: never start, clone, modify,
  move or delete it, or the HOLD file.
- **R2 Instruction files.** Never edit `CLAUDE.md`, the `## Current Priority`
  banner of `.ralph/fix_plan.md`, or this section. If a rule looks wrong or
  obsolete, write an escalation block. Status facts go in milestone task entries
  and design docs.
- **R3 No reverts.** Never revert or discard a PG-faithful change — landed or
  still uncommitted — because a parity/category number or runtime got worse. Keep it, file the regression as
  its own task naming the queries, escalate. Reverting is the owner's call. (A
  values mismatch — wrong rows — still stops *your* uncommitted change.)
- **R4 Owner decisions.** A deferral or decision recorded "by user/owner
  decision" is reopened only by the owner. If its reopen condition looks met or
  obsolete, file the evidence as an escalation block and stop.
- **R5 No bypass.** No `--no-verify`/`-n`, no `GOOPG_SKIP_PRECOMMIT=1`, no
  `git revert`, no `pkill -f goopg`, no changing `RALPH_LOOP` or
  `core.hooksPath`, no editing the hooks, guard scripts, `.claude/settings.json`,
  `.ralph/PROMPT.md` or `~/.ralph/`.
- **R7 Owner ledger rows.** A `.ralph/deferral_ledger.md` row containing
  `OWNER DECISION` is superseded only by the owner; the ledger is append-only.
- **R8 Hygiene.** No directories at the repository root, no committed build
  artefacts, no `rNNN-*` directories.
- **R6 Tuning is forbidden.** A constant chosen so an estimate or plan matches
  PG is rejected. B1 (`minimize_datum`/packed retention) is NO-GO and out of scope.

`RALPH_LOOP=1` hooks enforce R1, R2, R5 (`scripts/ralph-bash-guard.sh`,
`.githooks/pre-commit`, `.githooks/commit-msg`). A hook denial is a rule, not an
obstacle: do not look for another command that achieves the same effect.

### S — Selection
- **S1** The banner is the only ordering authority. Take its first selectable
  item; inside an item, follow its stated order.
- **S2** A newly found defect that loses data, returns wrong results or corrupts
  a shared resource: file it as a task, add an escalation block naming it, and
  continue with the banner. The owner places it in the banner (normally at
  position 0). Do not reorder the banner yourself.
- **S3** Every new task carries `Kind: recon|impl` and `Parent: <task-id|none>`;
  every task completed from now on carries `Movement: yes — <evidence>` or
  `Movement: none`. Write each field **at the start of its own line** — a field
  buried mid-sentence does not count and the guard rejects it.
  *Movement* = PG match count changed, or a `CATEGORIES-EXCL-MATCH:` category
  moved beyond ±3, or `make ea-ratchet` findings decreased. `Movement: yes` must
  cite one of those three instruments **with a number** on the same line;
  anything else (a unit test now passing, a feature now working) is
  `Movement: none` — real work, but not movement toward this goal.
- **S4 Lineage budget.** After **5 consecutive** completed descendants of one
  root with `Movement: none`, do not select or file another descendant. Write an
  escalation block into the root (what was tried, what each step proved, the
  blocker, expected movement if unblocked with named queries/categories, size),
  mark the root `[!]`, select elsewhere. Only the owner reopens it.
  Renumbering or re-filing does not reset lineage.
  `scripts/ralph-lineage-guard.py` enforces this at commit.
- **S5** A recon may file an implementation task only if the task names its
  expected movement (queries, categories or ea-ratchet findings vs PG) and how
  it will be measured.
- **S6** Tasks under the banner's `FROZEN-PREFIXES:` or marked `[!] FROZEN` are
  not selected, re-statused, deleted or given children.
- **S7** An open task the banner does not name is selectable only when every
  banner item is exhausted or blocked.
- **S8 Nothing selectable** (all remaining work blocked on the owner): write one
  escalation block into `.ralph/working_set.md` listing each blocker, and end
  the loop without code changes. Do not invent work.

### C — Changes
- **C1 Recon** commits touch no non-test file under `internal/` or `cmd/`
  (comments and env-gated traces included). **Adding a trace — even one fully
  inside an existing `if …TraceEnabled` guard — makes the task an impl task**:
  file one with `Kind: impl`, run the gates, and say so. The hooks read the
  task's `Kind:` from `.ralph/fix_plan.md`, not the commit subject.
- **C2 PG citation.** Every change claiming PG-faithfulness cites
  `./postgres/<file>:<line>` in its design doc.
- **C3 Absorption (B2).** Where goopg and PG differ irreducibly (Datum 48 B vs
  MinimalTuple, Go map overhead), the cost model receives the **PG-equivalent
  quantity**; the executor allocates what Go needs; a test pins the separation.
  The substituted quantity is (a) derived and written in the design doc
  **before** any parity number is taken — never chosen among candidates by
  result — and (b) a port of an expression that exists under `./postgres/`.
  No PG counterpart → ledger row "unabsorbable", not a chosen number. An
  absorption that lets the planner charge less than the executor needs must
  pass `scripts/tpch-acceptance-arm.sh` before landing. Not for C3/K63 display
  defects (repay those).
- **C4** `GOOPG_GATHER_PATHS=all` stays default (B3).
- **C5 Default-off arms.** A new default-off arm names, in the same commit, the
  task and measurement that decide it. It resolves to *promote* or *delete*;
  HOLD is not an outcome. At most ~4 at once.
- **C6** A production commit claimed unreachable/inert includes the evidence
  (a trace count of zero over the full corpus) **and** still runs the values
  gates. "Inert by default" is a claim, not evidence.

### G — Gates and measurement
Run on the change, in the task that changes production code:

| gate | when | pass |
|---|---|---|
| `scripts/tpch-spotcheck.sh` | any planner/executor change | exit 0, Q12/Q13 canonical |
| `scripts/tpch-acceptance-arm.sh` | statistics, costing or executor change | digest byte-identical |
| `scripts/tpcds-sf025-regression.sh sweep` | any planner/executor change | `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| floor capture + `pg-plan-parity-diff.py` | production planner/stats change | floor held |
| `make ea-ratchet` | estimate/selectivity/stats change | PASS, or ledger row **and** owning task per NEW finding class |

- **G1 SKIP-BLOCKED (exit 3) and any non-run are failures**, not passes. If a
  gate cannot run, produce: the command and failure text, the substitute gate
  and why it covers the risk, and a ledger row + `[ ]` task for the re-run. A
  gate this loop broke blocks every later production task until cleared.
  A commit may land on a SKIP-BLOCKED stamp **only** when the owner has listed
  that task id and gate in `.ralph/gate-exceptions.md` (with a commit cap and an
  expiry) and the body carries a `ledger:` line. The loop never edits that file
  and never generalises one task's exception into a standing one; without a row,
  a blocked gate means: mark the task `[!]`, escalate, select elsewhere.
- **G2** Gate scripts write `tmp/gate-stamps/<gate>.json` (tree hash, binary
  sha256, result). `.githooks/commit-msg` rejects an M0137–M0145 planner/executor
  commit without matching PASS stamps and a `CATEGORIES-EXCL-MATCH:` line (or
  `PARITY: N/A — <reason>`).
- **G3 Captures**: private clone + binary built from HEAD, never a shared
  server. Pass `DATADIR`, `CAPTURE_ENGINE` and `GOOPG_EXPECT_BIN_SHA256` (the
  scripts refuse otherwise). TPC-DS: clone the SF0.25 data to a `55xx` port
  like `scripts/tpcds-sf025-regression.sh` does; PG side is `:65438` (read-only). TPC-H via
  `estimate-audit -plan-only` (not `capture-tpch.sh`: empty stats), **canonical
  mode `-serial=false` (parallel)** since 2026-09-20 — serial is a diagnostic
  variant only. Procedure: `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`.
  `psql`/`pg_ctl` exist only in `./postgres/local_install/bin/`.
  Report the goopg plan file's sha256; if it equals the previous capture's,
  prove you did not measure the same binary.
- **G4** `make ea-ratchet-repin` only when the corpus or dataset changes, never
  in a commit that changes code.
- **G5** A matching plan that times out lands with a ledger row (coverage loss);
  never raise a timeout to hide it. A values mismatch stops the task.
- **G6** Commit a gated code change only after its gates PASS for exactly the
  staged code (the stamp compares a hash of the staged `internal/`, `cmd/`,
  `go.mod`, `go.sum`). Order: stage the code → run gates → commit the code →
  commit the design doc / fix_plan in a separate `ralph(…)` or `docs(…)` commit.
  Gates may take more than one loop; keep the code uncommitted until they pass.
  Never substitute a cheaper gate. When a gate cannot pass because of an
  owner-blocked environment (e.g. the `:65433` hold), do not commit; mark the
  task `[!]` naming the blocker.
- **G7** A red gate is "pre-existing" only with proof: run it at HEAD with your
  change stashed and paste the output. Known: two `lostcancel` vet findings in
  `cmd/goopg/main.go`.
- **G8 Dual-pipeline transition (M0145).** `GOOPG_JOINTREE_PIPELINE=1`
  selects the jointree-first pipeline; the env knob is the sanctioned
  migration mechanism (precedent: `GOOPG_PGSHAPED_DP`, `unnestPreDP`).
  - Value gates (`tpcds-sf025 sweep`, `tpch-spotcheck`, acceptance arms)
    always run the **default** pipeline — a PASS under the knob never
    discharges them.
  - Plan-parity captures under the knob are **EXPLAIN-only** on private
    lanes: a plan the executor cannot yet run is recorded as evidence
    (its own divergence class), never silently suppressed or gated on.
  - A change that only affects the knob-on path still runs the default
    pipeline's gates — the knob-off pipeline must stay green the whole
    transition.
  - The knob is retired by M0145-0008's cutover; nothing else may add a
    second pipeline-selection mechanism.

### D — Done, deferral, reporting
- **D1** Deferring any part needs **both** a `.ralph/deferral_ledger.md` row with
  a resume point **and** a `[ ]` task that owns it. A recon is done when its
  measurement, design note and follow-up tasks exist.
- **D2 Report** (loop report and design doc; `N/A — <why>` allowed, silence not):
  1. `CATEGORIES:` and `CATEGORIES-EXCL-MATCH:` verbatim, plus MATCH count;
  2. `shape-delta` counts; 3. stats epoch of both arms (OFF baseline re-taken
     if a values sweep intervened);
  4. seam-decline census by class at a stated timeout
     (`scripts/seam-decline-census.py`); 5. planning route (PG-shaped search vs
     legacy constructor); 6. `Movement:` and `Parent:`; 7. wall time of every
  query whose plan changed (ledger row for any that got slower).
- **D3 Design doc**: one per task id, written when selected, at
  `docs/design/0100-0149/<task-id>-<slug>.md`, indexed in
  `docs/design/README.md` in the same commit, `Status:` updated in the commit that
  changes the task state. A follow-up writes its own doc; a doc over 800 lines is
  split before anything is appended.
- **D4** Raw artefacts go in `analysis/m01NN/`, committed with the doc citing
  them. No `rNNN-*` directories, no root-level directories, no build artefacts.
- **D5 Commit area**: a commit touching no non-test file under `internal/` or
  `cmd/` uses `docs(…)`, `analysis(…)` or `ralph(…)`; one touching `internal/`
  never does.
- **D6 Reading**: `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/README.md`
  → `01` → `02` → the milestone doc. The claims listed under "Known-stale
  claims" in the background doc are false at HEAD — do not act on them.
  Enter `plan_parity_fix_take2/r*` directories only via its INDEX files or an
  explicit citation. Ledger rows: the last row for an id is current; `status`
  is unreliable — verify against the tree (`git log -S`).
<!-- PLAN-PARITY-HARNESS:END -->

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

## Benchmark clusters (TPC-H / TPC-DS)

Two separate stacks; full port/dir table and rules in repo-root `CLAUDE.md`.
TPC-H: `bench/tpch/` (goopg :65433, PG reference :65432, HammerDB SF=1 load —
see `bench/tpch/README.md`). TPC-DS: `bench/tpcds/` (goopg SF=1 :65436,
SF=0.25 gate :65437, PG reference :65438 — see `bench/tpcds/README.md`;
lifecycle via `bench/tpcds/server.sh`). Ports 65434/65435 are reserved nightly
ci/batch clone lanes. Row anchors (`bench/tpch/spotcheck_expected.env`,
`ci/batch/tpch-row-anchors.csv`) are load-dependent — re-pin after any TPC-H
reload.

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