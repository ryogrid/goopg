
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
- **As of 2026-09-14 the banner ranks the plan-parity group M0137–M0143 first**
  (M0137 -> M0138/M0139/M0140 -> M0141/M0142 -> M0143). **M0143 is gated on
  nothing — select it whenever everything above it is blocked**, which is what
  prevents a stall when M0141/M0142 expose only their recon task. Below the
  group come M-NIGHTLY's own items, then the pre-existing milestones.
  **M-NIGHTLY's *filing* obligation is unchanged and still unconditional** —
  every loop reads `ci/logs/action-items.md` and files each new `## AI-`
  subject — but M-NIGHTLY items are no longer *selected* ahead of the group.
  Two carve-outs still preempt: an item that breaks the build, and an item that
  breaks a gate the group depends on. Before selecting any M0137–M0143 task,
  read §"Plan-parity harness — applies ONLY to M0137–M0143" below; it is
  binding.
- Search before assuming something is missing. Prefer reading the spec and
  the upstream source over guessing.
- Land a design doc alongside or just before any non-trivial subsystem. This
  is a hard requirement, not optional documentation. **Overridden for
  M0137–M0143 only** — see §"Plan-parity harness" below, where the design doc is
  written when the task is *selected*.
- For any non-trivial subsystem item, create/update the corresponding
  `docs/design/<milestone-or-spec-id>-NNNN-*.md` file and update
  `docs/design/README.md`
  in the same loop and commit. (This same-loop indexing requirement is **not**
  relaxed for M0137–M0143.)
- Do not keep bare `NNNN-*` placeholders in active tasks. Replace them with
  concrete `<id>-NNNN-*` filenames before implementation begins. **Overridden
  for M0137–M0143 only**: the filename is reserved at task selection, not at
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

## Plan-parity harness — applies ONLY to M0137–M0143

**Scope fence.** Everything in this section applies **only** while working a task
in the plan-parity milestone group **M0137–M0143**. Outside that group,
`## Loop discipline (for Ralph)` and its design-doc rules apply unchanged. Where
this section and those rules disagree, this section wins **for these seven
milestones and nothing else**.

### The goal

Every currently executable TPC-H (22) and TPC-DS (99) query must produce **the
same plan as PG 18.3**, reached by the **same statistics**, the **same cost
computation** and the **same planning logic**. Never by forcing shapes.
**Within this milestone group only**, a slower plan that matches is **not** a
regression — execution time is reported, never adjudicated. (Outside the group,
performance regressions are judged normally; this clause must not be
generalised.)

State at the group's filing (2026-09-14): TPC-H **6/22**, TPC-DS **2/99**.

The group's immediate objective is to complete
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
with the owner's two decisions applied.

### The two owner decisions (2026-09-14) — binding, and they override 04

| question | answer |
|---|---|
| **Q1** — build executor-side narrowing, or accept a ~6–7/22 TPC-H cap? | **(a) build it** (M0139). And **(c) split the goal = Go**: TPC-DS work (M0140) proceeds independently of it. |
| **Q2** — should goopg reproduce PG's estimation errors? | **YES** — *"because otherwise identical plan generation is impossible."* (M0138) |

Two things follow that a reader of 04 alone would get wrong:

- **04 §1.2's proposed `PARITY-BLOCKED-BY-ORACLE-ERROR` rule was put to the
  owner and REJECTED.** Do not implement it and do not cite it.
- **R79's verdict (b) — "keep the superior statistics, close Q4 elsewhere" — is
  OVERTURNED.** Do not cite it to decline sampling work.

**Q2 is not a licence to fudge.** The goal statement already requires the
**same statistics**, so goopg's divergent ANALYZE sampler is a
PG-incompatibility and removing it is faithfulness work of the same kind as
porting a cost term. The method is: **port PG's `acquire_sample_rows` and let
its output be whatever it is.** Never tune a constant toward a target number,
never special-case a query, never add a fudge factor to "reach" PG's estimate.
**A task whose diff contains a constant chosen to make an estimate match is
rejected.** A number matched by tuning proves nothing about the mechanism and is
exactly the arbitrary forcing the goal forbids — the same reasoning K92 applies to a
mislabelled plan node, one layer down.

### Design docs — timing override for this group

**Write the design doc when the task is selected**, not before the milestone
starts: `docs/design/<task-id>-NNNN-short-slug.md`, status `draft` -> `accepted`,
indexed in `docs/design/README.md` **in the same commit**. This follows the
M0134 precedent and **overrides** three rules for this group only:
`docs/milestones/README.md` §"Workflow Per Milestone" step 2 ("write the design
docs listed under Required Design Docs first"), this file's own "reserve a
concrete design-doc filename before coding", and `.ralph/PROMPT.md`'s
"Reserve a concrete design-doc filename before coding". There is no up-front
Required-Design-Docs list for M0137–M0143.

The same-loop, same-commit indexing requirement is **not** relaxed.

### What to read, and what not to read

The previous phase left **126 round directories** under
`docs/design/not_ralph/plan_parity_fix_take2/` (`r0-*` … `r130-*`). They are raw
evidence, not guidance, and browsing them is how R127 was withdrawn on nine
findings, three fatal, **every one refuted by a document already on disk there**.

Read in this order, and stop when answered:

1. `METHODOLOGY3/README.md` — goal, current score, the frontier, the decisions.
2. `METHODOLOGY3/01-what-we-learned.md` — the 20 durable findings, plus Part C
   (claims that were established and then refuted).
3. `METHODOLOGY3/02-open-problems.md` — blockers, correctness risks, the
   numbered `N*` items the milestone tasks cite.
4. `METHODOLOGY3/03-process-retrospective.md` / `04-forward-plan.md` — method
   and plan, **read with the decisions above applied**.
5. The milestone doc under `docs/milestones/01NN-*.md`.

**Enter a round directory only via `INDEX-by-query.md` / `INDEX-by-mechanism.md`
(built by M0137-0008), via an explicit citation in METHODOLOGY3, or via a path
named in an M0137–M0143 task line** (the third exception exists because
M0137-0001 must open `.../r2-instrument/` to move the capture scripts, and the
index does not exist until the eighth task). Never browse
`plan_parity_fix_take2/` to find out what was tried — that is what the index is
for, and citing it is a scope gate.

`METHODOLOGY3/README.md`'s "Review record" lists what an adversarial review
already corrected in those documents; read it before treating any METHODOLOGY3
claim as novel.

### Known-stale claims — do not act on these

Each of these is contradicted by the tree at HEAD or by a later measurement:

- **K26 §9.3** describes the join-search seam as constants-only. It is a
  **pre-R51 snapshot**; `joinsearchseam.go:461` calls
  `inferTransitiveEqualities` unconditionally and both Slice-3 tests were
  adjudicated against PG. Only the **costing** half of join-order is open.
- **R35 FINDINGS §1** — "no estimate change can ever move a parity verdict" is
  **false**. R30, R31b and R36 each moved categories. The accurate statement is
  that an estimate change registers only insofar as it changes plan *structure*.
- **The R43-era "5 failing tests, 4 needing adjudication" list** is ~87 rounds
  stale; at least one member was re-baselined by R51. Re-measure (M0140-0001).
- **`bench/tpch/plans-pg/`** is a stale, serial fixture and is **not** a parity
  target (K9). The TPC-DS sibling's standing is M0137-0004's subject.
- **"Four default-off cost arms"** — there are **three** at HEAD; R128 promoted
  `GOOPG_NARROW_COST_INPUTS` to default ON.
- **`METHODOLOGY.md` §2 and `ROADMAP-to-all-match.md` §1–2 numeric tables** were
  superseded by `METHODOLOGY2.md`, which was superseded by `METHODOLOGY3`.
- **`04-forward-plan.md` §1.2's proposed rule** — overruled by Q2 above.
- **`make plan-gate` does not diff against live PG.** It is a
  goopg-vs-committed-goopg baseline pin (`Makefile:431-453`). The claim that it
  cannot pass until the goal is met appears in two round reports and is wrong.

### Way of working — this is a Ralph loop, not the R-round cadence

The previous phase ran an interactive cadence: SCOPE -> agent review -> commit ->
implement -> REPORT -> review -> commit, one numbered round per step. **Do not
reproduce it.** Concretely:

- **Do not create `rNNN-*` directories.** New raw artefacts go under
  `analysis/m01NN/`; conclusions go in the task's design doc.
- Ralph's own discipline applies: **one task per loop**, the working-set baton,
  the pre-commit gates, `make ralph-state-guard` before the status block.
- 04's two-tier model maps onto tasks, not rounds. A **recon task** is
  measurement plus a design note plus (where something is deferred) a ledger row,
  with **no production change** — `M0138-0001`, `M0141-S0` and `M0142-0001` are
  recon tasks, and a production diff in their commit is a scope violation. Every
  other task is an implementation task and lands its own gates.
- The two most productive results of the previous phase were cheap recons run
  outside the heavyweight cadence. Prefer eliminating a hypothesis in one task
  over scoping a campaign around it.

### Measurement — pipeline, ports, and what the metric cannot see

**Bootstrap first.** `psql`, `pgbench`, `pg_isready` and `pg_ctl` live ONLY in
`./postgres/local_install/bin/`, never on a default PATH, and every bench port
has its own user/password. The exports, the port->db/user/password table and the
working `estimate-audit` / `tpch-runner` / `pg-plan-parity-diff.py` command lines
are in §"plan-parity-take2 work appendix" at the foot of this file — **read its
§0 and §1 before running anything**, including `make plan-gate` (without the PATH
its `pg_isready` probe fails as *command not found* and the gate misreports the
server as unreachable). That appendix is procedural reference for this group;
where its round-cadence framing conflicts with §"Way of working" above, this
section wins.

**Canonical captures** live in `scripts/` after M0137-0001; until that lands,
use the appendix's commands directly (M0137-0003 replaces them with one
documented procedure). Two facts that have each cost a round:

- **TPC-H baselines come from `estimate-audit -plan-only`, not
  `capture-tpch.sh`** — the latter opens a fresh session per query and never
  ANALYZEs, so it captures TPC-H plans on **empty statistics** (goopg's ANALYZE
  results are per-connection).
- **`-serial` defaults to `true`** (`cmd/estimate-audit/main.go:285`), setting
  `max_parallel_workers_per_gather = 0` on **both** engines. That is why TPC-H
  `parallelism` reads 0: the category is measured **out**, not solved. The last
  real reading was 18->16 under the gather-paths flip.

**Ports** (see also `CLAUDE.md`): PG TPC-H `:65432`, goopg TPC-H `:65433`, goopg
TPC-DS SF0.25 `:65437`, PG TPC-DS `:65438`. Throwaway servers use `55xx` with a
private data clone. The `:6543x` block is shared — **verify a reference, never
restart it.**

**Server traps** (each earned): verify the serving binary by inode **and** by
behaviour — the inode check alone is insufficient, and a stale `tmp/` binary
once produced a false 18-query diff. `pg_isready` READY is necessary, not
sufficient: a surviving older server answers the port while your instance never
binds. **Never `pkill -f goopg`** (it self-matches the invoking shell, exit 144).
Always go through the cgroup cap wrapper (`scripts/goopg-test-run.sh`).

**What the parity verdict is blind to (K50).** `scripts/pg-plan-parity-diff.py`
normalises `cost=`, `rows=` and `width=` out before comparing, strips `::type`
renderings, and compares quals by (columns, operator multiset) rather than by
literal values. **An estimate, cost or width change registers only insofar as it
changes plan STRUCTURE** — R34 corrected a 580x cardinality error and measured
exactly zero. This is why a cost or statistics task is judged by category
movement plus `shape-delta`, never by estimate movement alone.

### The toward-oracle hazard — read before M0138 and M0142

Moving an estimate or a cost **toward** PG is the change class that has already
produced the programme's worst runtime regressions, because goopg's executor
does not have PG's mitigations:

- **B6** — R59 repriced index probes toward PG's constants, a correct and
  PG-faithful change, and TPC-DS **Q72 went from 4 s PASS to a 320 s TIMEOUT**:
  goopg has no Memoize on the NL probe path PG plans that shape with. Carried
  unfixed since. Expect this class, do not be surprised by it.
- **B8** — `indexProbeCostMultiplier = 2.0` deliberately departs from PG because
  goopg's executor materialises the whole TID list eagerly. **At `mult = 1` the
  DP picks PG-shaped NL plans that run 2–3x slower** (Q7 5.86 s -> 15.72 s). This
  is the known parity-vs-runtime knob; touching it is a cross-layer programme
  that has never been scoped, and K73's `Join.FromOuterReduction` is its sibling.
- **B10** — `indexCorrelationFor` returns 0 when the leading column has no
  correlation slot, pricing **every** such index scan at `max_IO_cost`; and
  R30's residue is that ANALYZE never visits indexes, so `estimateIndexGeometry`
  **synthesises** relpages/reltuples/tree_height. Both shift index-probe pricing
  corpus-wide and both sit directly under M0142. M0138-0004 populates
  correlation slots as a side effect — re-measure these two before concluding
  anything about index-scan costs.

**Time every query whose plan changed**, and file a ledger row for any that
regresses. Per the precedence rule below, a slower matching plan lands; an
unmeasured one does not.

### What every M0137–M0143 task report must contain

In the loop's report and in the task's design doc:

1. **Category movement**, reported as `blocked-excluding-matches` alongside the
   raw tool line (a MATCH can carry a category tag, so the raw count overstates
   "blocked").
2. **`shape-delta` counts.** A task with `shape-changed = 0` moved no plan at
   all; one with shape changes and no category movement moved plans **sideways**.
   Conflating the two produced a wrong conclusion once already.
3. **The declared stats epoch** for both arms, and confirmation that the OFF
   baseline was re-taken if a values sweep intervened.
4. **A seam-decline census by class, not by total**, at a stated timeout — a
   class can be *converted* rather than removed, and censuses are only
   comparable at equal timeouts.
5. **Which planning route the query took** — the PG-shaped path search, or the
   legacy/prebuilt constructor. `tryJoinSearch` preserves the syntactic node when
   `tryPGShapedJoinSearch` declines, and forced `join_collapse_limit=1` forms
   never reach the path-cost seam at all. A whole class of experiment was
   invalidated by measuring the wrong route.

### Success criterion

**Do not write a success test of the form "the match count rises."** No single
fix flips a query: at the group's filing, non-matching queries differed from PG
in several categories at once. Progress is **category movement**.

**Keep the non-regression floor**: TPC-H match >= 6, and TPC-DS match >= the
canonical figure **M0137-0004** declares — the programme currently quotes both 2
(live-PG reference) and 1 (committed fixture), which is exactly what 0004 exists
to settle, so the TPC-DS half of the floor is **re-pinned when 0004 lands**.
Either way, none of the current matches may be lost. That floor is the one
match-count clause the record shows earning its place — R120's caught the loss
of Q10.

Values gates bind on every task: TPC-H digest byte-identical to the baseline arm,
TPC-DS SF0.25 sweep all-zero. **A values break stops the task.**

**Precedence when a matching plan is slow enough to time out.** These two rules
collide, and the collision is not hypothetical — B6 records a *toward-oracle*
repricing taking TPC-DS Q72 from 4 s to a 320 s TIMEOUT, and M0138 and M0142 are
exactly that class of change. The precedence is: **a matching plan that times out
is not a parity regression, but it IS a coverage loss.** Land the plan, file a
ledger row naming the query, the timeout and the suspected executor gap, and
report the query in `Gates run:`. **Do not raise the timeout to hide it**, and do
not revert a PG-faithful change solely because it got slower — execution time is
reported, never adjudicated. A values *mismatch* (wrong rows) is a different
thing entirely and always stops the task.

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