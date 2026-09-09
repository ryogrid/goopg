# Handover: OC session 2026-09-09 → CC (plan-parity-fix-take2)

Reader: a coding agent who also reads `TODO.md`. This file covers SESSION
STATE ONLY — branch position, uncommitted work, servers/binaries/clones,
evidence locations, traps, and where to resume. It deliberately does NOT
repeat round status, K-items, or the roadmap (those live in `TODO.md` and
are authoritative there).

## 1. Position

- Branch: `plan-parity-with-pg-take2`. HEAD `bbd28c865` (R22 decline
  docs). All my session work except §2 is committed and pushed.
- Goal rule reminder (differs from the old workstream): identical plan +
  slower time is NOT a regression. Values gates still bind.

## 2. Uncommitted work (the ONLY delta in the tree)

`git status` shows exactly three modified source files (plus two
pre-existing submodule pointer modifications, `postgres` and
`third-party/tpcds-postgres` — NOT mine, do NOT touch or commit):

- `internal/optimizer/createplannl.go` — **R25 slice 1**: the NLI search
  arm emits `Join{Algo: NestedLoop, Lateral: true}` + parameterized
  `IndexScan` with OuterColumnRef keys (new helper `outerParamKey`);
  memoized shape dispatches to `createNestLoopIndexJoinPlanFused`
  (pre-decomposition body, verbatim move). Plus the bitmap arm is
  UNTOUCHED (still fused — deliberate, see DESIGN §6 open questions).
- `internal/optimizer/createplannl_test.go` — the two NLI-shape tests
  updated to the lateral-Join spelling (probe-key-outer-coordinates,
  residual-merged-coordinates, leaf-cond absorption).
- `internal/optimizer/joinsearchnlicost_test.go` — new `findLateralNLI`
  helper; `TestPGShapedSearchPicksNLIOnCost` asserts it. `findNLI`
  itself untouched (rule-path tests still need the fused type).

R22 is FULLY reverted — `pathindexordered.go` restored,
`pathindexplain_test.go` deleted. If any R22 residue reappears,
`git diff --stat` must show ONLY the three files above.

## 3. Live servers, binaries, clones (all mine; peers active)

| port | binary (exact path) | data dir | purpose |
|---|---|---|---|
| :5545 | `/home/ryo/work/goopg/goopg/tmp/goopg-r25s1` (== working tree) | `/tmp/pp2/tpch` (2.0G clone) | TPC-H gates |
| :5546 | same `tmp/goopg-r25s1` | `/tmp/pp2/ds05` (2.2G clone) | TPC-DS gates |

- STALE, do not use for gates: `tmp/goopg-r25base` (pre-R25 stash
  build), `tmp/goopg-pp2*` (R21/R22 era), `tmp/goopg-parity-r0`.
  Rebuild from the tree instead; the tree has moved since.
- FOREIGN, do not touch: shared :65433 runs `tmp/goopg-bench-bin`
  (started 9/8 17:05 by someone else); PG refs :65432 (tpch) and
  :65438 (ryo@tpcds05) are live — verify, never restart. Peer goopg
  processes `tmp/goopg-splice` (:5544!) and `tmp/goopg-k26` (:5543!)
  exist — my R0-era notes saying "my :5543/:5544" are STALE; my lane
  is :5545/:5546 and `/tmp/pp2/*` only.
- Stop mine with `<bin> stop -D <dir>` (never pattern-kill).
- Launcher with inode verification (K5):
  `docs/design/not_ralph/plan_parity_fix_take2/r1-qpqual-index/launch-verified.sh
  <bin> <dir> <port> <log> <scope>`.
  My scratch foreground launcher: `/tmp/pp2/launch.sh` (same contract:
  backgrounds internally, waits for readiness in foreground, unique
  scope per launch).

## 4. R25 slice-1 evidence (where it is, what it says)

- Suites: `go test ./internal/optimizer/ -count=1` green,
  `./internal/executor/` green (9.2s). Always `-count=1` (stale-cache
  trap, §5).
- EXPLAIN byte-identical BOTH corpora (same clones, seed-pinned,
  GUCs pinned work_mem=64MB/max_parallel_workers=4):
  TPC-H 22/22 (`/tmp/pp2/tpch-base.sections.txt` vs
  `.../tpch-r25s1.sections.txt`, base binary built from a stash of
  exactly these 3 files); TPC-DS 99/99 (`/tmp/pp2/ds05-base.plans.txt`
  vs `.../ds05-r25s1.plans.txt`).
- TPC-H digest 24/24 MATCH (`/tmp/pp2/tpch-digest-r25.log`,
  `tpch-runner -diff` vs an older baseline log).
- TPC-DS sweep (own binary via `GOOPG_BIN`+`SF05_NO_BUILD=1` on the
  SHARED cluster — sanctioned path):
  `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260909-083242.txt`
  → **PASS=93 MISMATCH=0 ERROR=0 TIMEOUT=2 (Q30, Q81) SKIP=4**.
  Its status-delta vs the 09-08 baseline ALSO shows Q30/Q81
  PASS→TIMEOUT plus Q54/Q64/Q11 slowdowns — but that baseline
  (`9112c762c`) predates many peer commits, so **NOT attributable to
  slice 1**. Before slice 2, run a controlled pre/post-R25 A/B on
  Q30/Q81 (same data, same binary pair as §4's EXPLAIN A/B): if the
  lateral driver re-opens probes pathologically there, slice 2 must
  answer it, not inherit it.

## 5. Traps (session-specific; TODO.md owns the timeless ones)

- **`go test` result cache goes stale across file add/remove.**
  A deleted test file still failed until `-count=1`. Always
  `-count=1` for verdicts (never for gates that forbid it — none
  here; the repo's never-`-count=1` rule is about THEIR gate
  scripts' result cache, not correctness re-runs).
- **cgroup scope names are single-use.** Reusing a scope name fails
  with "was already loaded" and the server start silently never
  happens — while the OLD server (or none) still answers the port.
  Unique scope per launch (K5's launcher does not do this for you).
- **pg_isready READY is necessary, not sufficient** (K5): verify
  `/proc/<pid>/exe` inode == built binary (launch-verified.sh does).
- **Background execution is banned by the goal** (mine and yours):
  no `&`/`setsid`/`nohup` on any invoked command line. Put
  backgrounding INSIDE a script file (precedent:
  `bench/tpch/setup_goopg.sh`, `/tmp/pp2/launch.sh`) and wait in
  the foreground. Per-query `timeout N` + record-rc-and-continue
  for captures and sweeps.
- **Capture formats that the diff tools require:**
  TPC-H sections are `=== Qn` with `Q15` REPLACED by
  `Q15a-VIEWBODY` (body from `Q15ViewBody()`, NOT the CREATE VIEW);
  Q15b-MAIN has no PG fixture and is out of scope on both sides.
  TPC-DS sections are `===== Qn =====`; multi-statement files
  (14/23/24/39) need the per-statement EXPLAIN-prefix split (exactly
  as `sf05_capture_plans` does it); Q36/70/86 are shared parse
  failures on both engines. Temp SQL filename must be FIXED
  (`parity-capture-$$.sql`), never mktemp — the path lands in psql
  ERROR text and fakes diffs (K18).
- **Pin GUCs in-session** (`SET work_mem='64MB'`,
  `SET max_parallel_workers_per_gather=4`) and
  `GOOPG_ANALYZE_SEED=20260905` at server start; benchmark env files
  already do the latter for THEIR servers, private launchers must do
  it themselves.
- **Commit hygiene:** stage by explicit pathspec (foreign WIP
  present); `*.out` is gitignored — name diff outputs `*.txt`;
  `commit -n`, push `plan-parity-with-pg-take2`.

## 6. Resume here

1. Q30/Q81 pre/post-R25 A/B (timing + values on the :5545/:5546 pair;
   binaries already exist: `tmp/goopg-r25s1` vs a fresh stash-build).
2. Then R25 slice 2 (executor driver) per
   `r25-nli-decompose/DESIGN.md` §§2–5; the open questions in §6
   (multi-key/SAOP keys, BitmapHeapScan inners, deform bounds) are
   load-bearing, not polish.
3. Do NOT redo: R0/R1 (landed), R21/R22 (declined with proof),
   R23 (stale, verified live). R22's `pathindexplain_test.go` is
   deleted — if a test run names it, suspect cache, re-run
   `-count=1`.
