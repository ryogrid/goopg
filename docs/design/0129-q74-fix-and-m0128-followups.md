# 0129 — Q74 fix + M0128 verdict follow-ups + residual-ledger burn-down: Implementation Plan (Ralph task breakdown)

| field | value |
| --- | --- |
| status | **planned** — filed 2026-08-08; no task started |
| date | 2026-08-08 |
| milestone | `docs/milestones/0129-q74-fix-and-m0128-followups.md` |
| design of record | Per task, the cited primary sources: deferral-ledger rows (`.ralph/deferral_ledger.md`), `docs/design/0128-0001-bitmap-heap-scan.md` §6, `docs/design/parallel-query/` (**07** §3.1, **10-roadmap** "Deliberately deferred" table, **IMPLEMENTATION-TODO** P8 follow-up note), `docs/design/0125-0055-routine-command-counter-and-self-modified.md`, PG 18.3 under `./postgres/` (read-only oracle). **This document is the task authority**; per-subsystem design docs are created within M0129 where the milestone doc's Required-design-docs table says so |
| convention | Tasks are sized for one Ralph loop (one session) completion (`.ralph/PROMPT.md` "ONE task per loop"). Each task lists its subtasks inline (user directive 2026-08-08: **no item is deferred without a strong reason recorded in the deferral ledger; subtasks live in the fix_plan task body, not in a forwarded note**). Each task lists its gate. Where a task proves larger than one loop, split it at selection time and record the split here and in fix_plan |
| decomposition source | User directive 2026-08-08 (the three bullet groups: Q74 fix; M0128 verdict follow-ups; residual ledger rows), mapped onto the ledger rows and design docs cited per task |

## 1. Positioning

Every priority milestone through M0128 is CLOSED (banner `f18d3014`,
2026-08-08). M0129 is the **top-priority milestone** (user directive
2026-08-08), ahead of the M-NIGHTLY backlog; the standing M-NIGHTLY *filing*
obligation (read `ci/logs/action-items.md`, file each new `## AI-` subject)
still applies to every loop, but selection goes to M0129.

**Ordering principle (inherited from M0128):** the live default-ON regression
first (S1); correctness/robustness gaps before performance features (S2, S3,
S6, S10 before S4, S5); measurement-only tasks (S7) may interleave whenever
the host is quiet. S6 subsumes S3's defect class (a tuple-carried ctid cannot
be lost by spill) — if S6 lands first, S3 reduces to verifying the spill shape
rides the column and closing its ledger row.

## 2. Common gate vocabulary for all tasks

Same vocabulary as M0127/M0128 (binding): **UNITS**
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`), **SMOKE**
(pre-commit pgbench hook; never `--no-verify` in the loop), **SPOT**
(`scripts/tpch-spotcheck.sh`, Q12=2/Q13=35 canonical, fresh capped server),
**DS05** (`scripts/tpcds-sf05-regression.sh sweep`; zero row/checksum
deltas), **PLAN** (`make plan-diff LABEL=…` against the current re-baseline),
**RACE** (`make race-gate` for shared-state stages), **SIBLING**
(sibling-path audit enumerated in review), plus `make ralph-state-guard`
before every finish. Timed measurements on a quiet host with server age held
constant (sweep-tail discipline; a server that just ran a timeout query sits
at GOMEMLIMIT and thrashes GC — never A/B against it). Never `-count=1` in a
gate's `go test`. Servers always under the cgroup cap
(`GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh … --listen 127.0.0.1:<port>`;
5533/5534 for throwaway experiments; the 6543x block is allocated). Every
implementation task runs in a git worktree off pinned clean HEAD, staged by
explicit pathspec, re-running its own named guard after any rebase/handoff.

## 3. Task decomposition

### S1 — Q74 path-selection fix (M0128-P0.1 second half) — 1–2 loops

**Source:** ledger row 2026-08-07 M0128-P0.1 (attribution complete, fix
pending). **This is a live, default-ON, stable ~7× regression** (SF0.5 Q74
11–14 s → ~86–99 s; SF1 ~290–329 s in nightlies) — the milestone's first
task.

**What is known (do not re-derive):** output is byte-identical (7 rows, ck
`2ffc13c77bf53028`); `buildRestrictInfos` classifies all three CTE self-join
equalities as `isEquijoin=true` with correct `leftRelids`/`rightRelids`;
`splitJoinClauses` returns keys=1 for every level-2 pair with a direct
equality; **hash join paths ARE generated**; the cost model ranks hash (~842)
< merge (~2100) < NL (~8.4M) at level 2, yet the final EXPLAIN is nested
loops at all 4 join levels — the rejection is **not** cost-driven.

**Subtasks (all in this task):**

- **S1.1 — diagnose.** Add targeted debug output
  (`fmt.Fprintf(os.Stderr, "M0129-DEBUG …")`) in `addPathsToJoinrel`
  (`internal/planner/joinpaths.go:139`): for each level-2 pair of the
  nprefix=4 search, print every candidate path's kind+cost and which path
  `setCheapest` retains. Run Q74 against the SF0.5 cluster
  (`bench/tpcds/server.sh start sf05`, port 65437) and identify which
  non-hash path wins each joinrel and why. The recorded hypotheses: (a)
  `addMergeJoinPath` computes a lower-than-expected cost for CTE scan inputs;
  (b) a precondition silently drops the hash path in `addHashJoinPath` (e.g.
  a nil `CheapestTotal` on one input); (c) the merge path's `Pathkeys`
  dominate hash on the non-cost dimension in `comparePaths`.
- **S1.2 — fix.** Implement the correction the diagnosis names; remove the
  debug output (or gate it behind an existing debug env flag).
- **S1.3 — pin.** Regression guard: a plan-shape test (unit or EXPLAIN
  golden) that a 4-way CTE self-join with equality clauses chooses hash
  join. Verify: Q74 SF0.5 ≤ 20 s with byte-identical output; DS05 full sweep;
  the ledger row and fix_plan item updated with the root cause.

**Gates:** UNITS + SPOT + DS05 (Q74 time is the headline number) + PLAN.

### S2 — `deleteWithUsing` EPQ — 1 loop

**Source:** ledger row 2026-08-06 M0125-0055. `deleteOp.deleteWithUsing`
(`internal/executor/operators_storage.go:6292`) currently does
`continue // skip concurrent-update EPQ for USING case` on a concurrent
update — a silently skipped row where PG waits for the updater, re-fetches
the live version via the `t_ctid` chain, re-evaluates the USING predicate,
and deletes that version (`nodeModifyTable.c` `ExecDelete` → `EvalPlanQual`).

**Subtasks:** mirror the loop `deleteOp.Next` already has (`epqWait` →
`epqFollowHOT` → `epqFollowChain` → re-evaluate `o.pred` and the USING
portion → retry the stamp), including the `epqRetryLimit`/40001 and
moved-partition arms; add an isolation spec (or extend eval-plan-qual)
proving the wait-and-delete-successor behaviour.

**Gates:** UNITS + the isolation family + SPOT.

### S3 — sort-spill ctid side-channel carry (root-0038) — 1 loop

**Source:** ledger row 2026-08-06 M-NIGHTLY (root-0038), first row. `sortOp`
drops the ctid side-channel the moment it spills (`ctidsDisabled`,
`internal/executor/operators.go:612-686`): a row-locking query whose sort
exceeds `work_mem` silently loses its tuple lock. PG has no such cliff — the
row mark is a resjunk ctid column that tuplesort carries through spills.

**Subtasks:** carry `sortCTID` into the spill records and back out of the
N-way merge (`sortOp.flushChunk` / `initMerge`, operators.go:798/882) — this
changes the on-disk sort record layout; add a guard test that forces a sort
past `work_mem`
(tiny `work_mem`) under `FOR UPDATE` and asserts the lock still blocks a
concurrent updater. **Note:** S6 (the resjunk-ctid column path) subsumes
this defect class; if S6 landed first, reduce this task to verifying the
spill shape and closing the ledger row.

**Gates:** UNITS + RACE + isolation family + SPOT.

### S4 — cooperative parallel hash build (the announced P2.1a/P2.1b) — 2 loops, design-doc-first

**Source:** parallel-query/10-roadmap "Deliberately deferred" row — reopen
condition **MET** by M0128-P2.1 (2026-08-07): build time is 12.6–41.0 % of
total for medium/large dimensions (part 200K 12.6 %, customer 150K 34.6 %,
orders 1.5M 41.0 %; measurement `analysis/m0128-p2.1-hash-build-measurement.md`).
The two candidate implementations (parallel-query/IMPLEMENTATION-TODO P8
follow-up note) were announced in fix_plan as "M0128-P2.1a/P2.1b" but never
filed as tasks — this task files them. **Design doc first** (milestone doc's
Required-design-docs table), status `draft` → `accepted` within M0129.

**Subtasks:**

- **S4.1 (= P2.1a) — producer/consumer split.** Parallelise the build side's
  scan+filter while one goroutine owns the hash map (07 §3.1). The Q13-class
  shape (build side carrying an expensive predicate over ~1.5 M rows) is the
  motivating measurement.
- **S4.2 (= P2.1b) — genuinely concurrent build** (sharded, or
  per-worker-partial-then-merge — what PG's barrier machinery buys). Take
  this only if S4.1's measurement still shows build dominance; otherwise
  record a measured not-needed verdict here and in the roadmap row.

**Gates (10-roadmap P8, per subtask):** identity over the join corpus;
race-gate under a probe-heavy workload; TPC-H Q9/Q17/Q19 measurement; UNITS
+ SPOT + DS05.

### S5 — bitmap heap scan burn-down — 8 subtasks, 1 loop each (group where trivially small)

**Source:** `docs/design/0128-0001-bitmap-heap-scan.md` §6 (8 deferral rows)
plus the P2.4 caveat: bitmap paths are generated but **always rejected by
`add_path`** — no production plan has ever used one. Base code:
`internal/planner/{pathbitmap,costbitmap,createplanbitmap}.go`,
`internal/executor/{tidbitmap,operators_bitmap}.go`.

**Subtasks:**

- **S5.1 — BitmapAnd/BitmapOr path generation.** Port PG's
  `choose_bitmap_and` (`indxpath.c:1785` — greedy selection of AND-able
  indexes) into the planner's bitmap path generation; the executor nodes
  (`bitmapAndOp`/`bitmapOrOp`) already exist (P2.3). Proof: EXPLAIN of a
  two-index query shows `BitmapAnd` with two `Bitmap Index Scan` children.
- **S5.2 — selectivity-region survival proof.** Construct a query (synthetic
  dataset is fine) whose selectivity makes the bitmap path beat both the
  index scan's random I/O and the seq scan's full-table cost in `add_path`;
  record the EXPLAIN and the measured time under `analysis/`. This closes
  the "generated but always rejected" caveat and is the milestone-level
  bitmap verdict.
- **S5.3 — correlation statistic + two-term Mackert-Lohman.** ANALYZE
  collects a `pg_stats.correlation` equivalent
  (`internal/executor/operators_analyze.go`; nearest neighbour: M0128-P3.1's
  `avgVarBytes` plumbing); `computeBitmapPages` gains the correlation term
  (full `cost_bitmap_heap_scan` formula); the same stat feeds
  `costIndexScan`'s correlation adjustment.
- **S5.4 — partial-index predicate recheck.** PG's `create_bitmap_subplan`
  appends the index predicate to `bitmapqualorig` so partial-index quals are
  re-evaluated when the scan goes lossy. **Actionable now** — goopg has
  partial indexes (root-0041 was precisely a partial-index selection bug).
- **S5.5 — `tbm_extract_page_tuple` bulk-offset extraction.** PG extracts
  all offsets for a page in one call; goopg iterates one at a time
  (`tidbitmap.go` iterator). Micro-optimisation with a microbenchmark.
- **S5.6 — parallel bitmap heap scan.** PG's `ParallelBitmapHeapState`
  analogue over goopg's `ParallelGroup` + `ParallelScanState` (the design
  doc §3.7 sketches the no-DSA shape: leader builds the bitmap, workers
  claim page ranges from the sorted iterator via a shared atomic counter).
- **S5.7 — read-stream prefetch.** PG 18's read-stream architecture
  (`read_stream_begin_relation` + `bitmapheap_stream_read_next`) feeds the
  I/O layer a window of future blocknos. **Named blocker:** gated on a
  general I/O prefetch layer, which does not exist. If it still does not
  exist at selection time, record that strong reason in the ledger (do not
  silently skip) and close the subtask as blocked-with-reason.
- **S5.8 — GiST/GIN `getBitmap` AM entry point.** **Named blocker:** the
  0128-0001 design states goopg has no GiST/GIN index AM to hang
  `amgetbitmap` on. At selection time, re-verify against HEAD (the GiST
  surface has moved before); if no usable AM exists, record the strong
  reason in the ledger and close as blocked-with-reason.

**Gates (per subtask):** UNITS + unit tests per mechanism + SPOT; DS05 for
anything plan-visible (S5.1/S5.2/S5.3/S5.4); RACE for S5.6.

### S6 — resjunk-ctid column path re-enable (M0128-P6.1 durable fix) — 1–2 loops, design-doc-first

**Source:** ledger rows 2026-08-06 (root-0038 second row / M0128-P6.1; the
column-path disable event is 2026-08-08). The
column path was disabled (`internal/planner/planner.go:1636` `numCtid := 0`)
because `wireRowMarkCtidColumns` (`planner.go:1993`) added ctid columns to
SeqScan schemas **after** parent nodes were built — in self-joins the ctid
leaked into the right child's output positions (eval-plan-qual `partiallock`
returned 0 rows). goopg's plan tree stores schemas eagerly at construction
time, so a post-hoc column requires recomputing every ancestor schema.
**Design doc first** (`0129-0003-*`, draft → accepted within M0129): choose
between (a) bottom-up schema recomputation after injection and (b) injecting
the junk attribute when the scan is first created (PG's
`preprocess_targetlist` timing).

**Subtasks:** re-enable the column path behind the chosen design; propagate
through EVERY intermediate node (Join, Filter, Sort, …), not just leaf scan
+ top Project; regression tests: eval-plan-qual `partiallock` +
`lockwithvalues` green, a self-join `FOR UPDATE` shape; verify the slot
side-channel can then retire or is kept as belt-and-braces (record the
decision).

**Gates:** UNITS + SPOT + ISOLATION (eval-plan-qual family) + DS05.

### S7 — clause-6 re-adjudication (M0128-P5.1 follow-up) — 1 measurement loop

**Source:** ledger row 2026-08-07 M0128-P5.1. The rendering gap is fixed
(`explain_names.go` `_1`/`_2` dedup); the estimate-audit comparison was never
re-run. **No engine change expected.**

**Subtasks:** run `scripts/tpch-estimate-audit-arm.sh m0129-s7-clause6 …`
(the label is a **positional** argument; the `cmd/estimate-audit` equivalent
is `--label m0129-s7-clause6`) on a quiet host
(needs a TPC-H goopg+PG cluster pair — ports 65432/65433 per
`bench/tpch/README.md`); compare the spine channel for Q2/Q8/Q17/Q18/Q22
against the pre-P5.1 baseline (`analysis/leftdeep-joins/` latest sweep); all
five should now be adjudicable (no `N ambiguous` marker). Record the verdict
under `analysis/` and close the ledger row.

**Gates:** the recorded measurement itself.

### S8 — statement-granularity command counter + per-tuple `cmin`/`cmax` — 2–3 loops, design-doc-first

**Source:** ledger row 2026-08-06 M0125-0055 (second row). Today
`Context.CmdID` advances per nested VOLATILE routine body (the six
child-`Context` sites in `plpgsql_runtime.go`); PG increments before *each*
command and carries per-tuple `cmin`/`cmax` in the heap header, which is what
would retire the data-modifying-WITH fence maps entirely. **This is the
milestone's largest item** — split at selection time if a loop cannot hold
it, and record the split here and in fix_plan.

**Subtasks:**

- **S8.1 — design doc** (`0129-0001-command-counter-and-cmin-cmax.md`,
  draft → accepted within M0129): heap-header `cmin`/`cmax` layout
  (catversion / on-disk compatibility considerations), transaction-owned
  per-statement `CmdID`, which visibility comparisons replace the
  fence/reveal-map lookups, and the migration story for existing clusters.
- **S8.2 — per-statement counter.** Increment in the routine-body statement
  loops in `plpgsql_runtime.go` and at the plpgsql SPI analogue.
- **S8.3 — per-tuple `cmin`/`cmax` + fence-map retirement.** Land the heap
  header fields and retire `CTEWriteFence`/reveal maps — or, if the heap
  format change is genuinely out of reach, record the strong reason in the
  ledger (a documented, attributed no-go is a recorded outcome; silence is
  failure).

**Gates:** UNITS + isolation family (the EPQ/fence behaviour must be
unchanged) + SPOT + DS05; RACE for the transaction-owned counter.

### S9 — `reduce_outer_joins` residuals — 4 subtasks, 1 loop each

**Source:** ledger row 2026-08-07 M0128-P4.1. Base code:
`internal/planner/reduce_outer_joins.go`. All four are pessimization fixes or
missed-demotion quality gaps, never wrong answers.

**Subtasks:**

- **S9.1 — strictness catalog.** Replace the hardcoded comparison-operator
  check (`isStrictCompareOp`, `reduce_outer_joins.go:141`) with
  `isStrictOp(oid)` consulting `pg_proc.proisstrict` (PG checks
  `func_strict(expr->funcid)` for arbitrary functions/operators).
- **S9.2 — ON-clause propagation.** `collectNonNullableTableNames` currently
  examines only WHERE-clause quals; collect from `JoinExpr.On` clauses and
  propagate per join type (PG `reduce_outer_joins_pass2`: inner joins merge
  with upper quals; outer joins pass local to the nullable side, upper to
  the preserved side).
- **S9.3 — LEFT→ANTI.** Port `find_forced_null_vars` (detect `IS NULL` on
  nullable-side columns) → LEFT→ANTI demotion.
- **S9.4 — RIGHT→LEFT flip half (+ FULL→RIGHT partial reduction).**
  **Named prerequisite:** unrepresentable today — `parser.FromExpr` is a
  Base RangeVar + flat `[]JoinExpr`, so a flipped tree has no syntax tree to
  hang on. The parser/AST extension to nested-join representation **is part
  of this subtask**. If scoping shows the representation change breaks
  unrelated planner invariants, record that strong reason in the ledger
  rather than silently skipping.

**Gates (per subtask):** UNITS + demotion-matrix unit tests + SPOT + DS05
(plan movement expected where demotions newly fire — adjudicate via the plan
channel).

### S10 — `ExecError.Pos` → wire `FieldPosition` — 1 loop + a baseline re-capture

**Source:** ledger row 2026-08-06 M0127-PS6.2. goopg never emits the
`FieldPosition` ('P') error field for executor errors, so psql shows no
`LINE n: … ^` caret where PG shows one. `writeQueryError`
(`internal/server/query.go`) already takes variadic extra fields; the COPY
path sets the precedent (`internal/server/copy.go:729`, 1-based).

**Subtasks:** at each `*executor.ExecError` site in
`internal/server/dispatch.go`, pass `protocol.ErrorField{Code:
protocol.FieldPosition, Value: strconv.Itoa(ee.Pos + 1)}` when `ee.Pos > 0`
(`ExecError.Pos` is 0 when unset; the COPY path's `se.Pos >= 0` guard relies
on `SyntaxError.Pos`'s -1 sentinel instead);
drop the regress-runner normalisation
(`internal/testport/framework/regress.go:157`) and **re-baseline the corpus**
(the M0106 six-silent-regressions precedent — a suite-wide baseline
re-capture is part of this task, not a follow-up); verify the caret in psql
with a failing expression.

**Gates:** UNITS + the regress suite re-baselined and green + SPOT.

## 4. Order

S1 → S2 → S3 → S6 → S10 → S4.1 → S4.2 → S5.1–S5.8 → S9.1–S9.4 → S8.1–S8.3,
with two relaxations: **S7 may interleave any time the host is quiet** (it is
a measurement, not an engine change), and **S6 may be taken before S3** (it
subsumes S3's defect class). S4.2 is conditional on S4.1's measurement; S5.7
and S5.8 carry named blockers and follow the blocked-with-reason protocol
(ledger row, never a silent skip).
