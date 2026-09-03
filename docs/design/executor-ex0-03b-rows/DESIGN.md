# EX0-03b Design — Per-worker rows/loops/time lines in EXPLAIN ANALYZE

Item: `TODO_EXECUTOR.md` EX0-03b (gate: golden test, pin `changed=0`,
no timing claim). Status: design for review. This builds the new
measurement path EX0-03 deliberately did not (its premise — "counting
already happens" — is false for worker rows/loops).

## 1. Mechanism (pattern reuse, not invention)

- `maybeInstrument` already hands the live scope to any op implementing
  `instrumentScopeCarrier.setInstrumentScope` (`instrument.go:244,357`;
  sole implementer today: `cteDMLPrefixOp`, `operators_cte_dml.go:104`,
  which reinstates it around late `Build()` via `buildUnderScope`).
- `BuildWorker(plan)` → `buildNode(plan)` (`executor.go:32-34`) runs the
  same `maybeInstrument` dispatch as the leader — so setting the
  package-global `instrumentScope` around `buildChild()` inside
  `runWorker` yields wrapped worker operators for free.
- `gatherOp`/`gatherMergeOp` TO-BE-ADDED `setInstrumentScope`
  implementations store the scope at their own `Build()` time (inside
  the top-level `withInstrumentation`, so it is live then). No such
  method exists on either file today — this is proposed, not described.

## 2. The four review-mandated answers (from EX0-03 review B1–B4, as
amended by this item's own review)

- (B1, shared-table collision AND global-scope race) The stored scope's
  table is NEVER reused, and the package-global handoff is NEVER racy:
  each execution site mints a FRESH `&instrumenter{timing: scope.timing,
  table: make(nodeStatsTable)}`, installed around its `buildChild()` by
  a new helper `buildUnderFreshScope` that holds a package-level
  `instrumentScopeMu` across set + Build + restore. The mutex serializes
  only tree construction (fast, no I/O); execution stays parallel. The
  CTE precedent's unguarded save/restore is safe there only because the
  CTE path is serial — workers are not, hence the mutex. Same plan keys
  across tables cannot collide (different maps); the global cannot leak
  across goroutines (mutex). Only the `timing` bool is inherited.
  `instrumenter` is `{timing, table}` only and nothing but the table
  outlives the helper, so freshness is sufficient.
- (B2, renderer carrier + prebuild verdict) Tables are collected into
  pre-sized indexed slots (`o.workerTables[idx]`, leader slot `n` — no
  mutex; indexes disjoint) and folded post-join in `Close` (after
  `group.Wait`) into a Context-keyed carrier
  `map[optimizer.Node][]workerNodeStat` with `workerNodeStat{Worker int,
  RowsOut, Loops, StartupNs, TotalNs int64}`. The fold copies ONLY these
  four fields (see B4). Prebuild/coop sites (`operators_gather.go:131`
  bitmap, `:198` shared-hash, `operators_gather_merge.go:101`,
  `parallel_hash_build.go:443`) build with scope explicitly NIL —
  uninstrumented, exactly today's behavior. Their throwaway trees must
  never touch leader/worker tables (prebuild drains would double-count
  the same plan keys; the bitmap tree is never even closed, so its
  `loops` would leak).
- (B3, coverage) Four instrumented sites: `gatherOp.runWorker`
  (`operators_gather.go:338`), leader build (`:296`),
  `gatherMergeOp.runWorker` (`operators_gather_merge.go:195`), its
  Open-time build (`:134`). (Zero workers is the absence of sites, not
  a site: no per-worker lines, only `Workers Launched: 0`.)
- (B4, determinism + collection invariant) Golden asserts
  presence/shape ONLY: `Worker 0:` … `Worker N-1:` lines present,
  worker-line count == launched count, identical SHAPE on rerun (exact
  per-worker `rows=` may differ — schedule-dependent). NO row-sum
  equality: it fails legitimately via leader participation (leader rows
  bypass worker tables), zero workers (`0 == N` impossible), LIMIT/early
  Close (counted-but-discarded rows), and nil-slot overcounting
  (`instrument.go:190-199` counts nil as a row; `runWorker` skips nils).
  Merge math is instead pinned by a deterministic unit test feeding
  synthetic tables to the fold. Collection invariant, narrowed:
  rows/loops/time are final at EOF+flush (touched only in
  `Open`/`Next`); `Close`'s `accountBuffers` mutates buf/WAL/memory
  only — irrelevant because the fold copies the four fields and nothing
  else. Collect any time post-EOF; the fold runs post-`Wait` regardless.

## 3. Out of scope

Per-worker Buffers/WAL/Memory (global-pool deltas, unattributable —
EX0-03 record stands); JSON twin (same escape clause as EX0-03: the JSON
path has no stats plumbing; text only, noted); VERBOSE-gating (recorded
divergence, not chased); `sharedHashBuild` worker attribution (EX5).

## 4. Verification (gate)

- New test in `explain_parallel_workers_test.go`: forced small parallel
  plan, `EXPLAIN (ANALYZE, TIMING OFF)` twice — worker-line count ==
  launched count on both runs, SHAPE identical on both runs (exact
  per-worker `rows=` may differ); plus a deterministic unit test of the
  fold over synthetic tables (exact carrier contents asserted).
- Existing suite green incl. all EX0-03 tests; serial plans byte-identical
  (no worker tables exist → no new lines → renderer output unchanged).
- Plan-shape pin for this item class: the walker change is unreachable
  when the carrier is empty, and the carrier is empty unless a Gather
  executed — pinned by a unit test asserting empty-carrier render is
  byte-identical, plus the construction argument above. Full
  goopg-vs-goopg `changed=0` re-runs at EX0-06 (first full baseline).
- `git diff --stat`: `internal/executor/` only (+ test), no new
  dependencies, no timing claim.
