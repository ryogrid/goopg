# plan-parity-fix-take2 — TODO / progress ledger

Goal: every currently-executable TPC-H and TPC-DS query produces the SAME
plan as PG 18.3 — via the SAME statistics, the SAME costing, the SAME
planning logic. Never by forcing shapes. Authoritative diagnosis:
`plan-parity-root-causes.md` (rev 2) in this directory.

Process per round (binding): Design Doc under this dir → agent review →
reflect → `commit -n` + push → implement → English results report in this
dir → `commit -n` + push. Investigation/review/implementation may be
delegated to subagents; **all program execution is FOREGROUND** (a missed
failure/hang in background wastes the session — goal instruction).

## Policy notes (differ from the previous workstream — read first)

- **Same plan ⇒ timing delta is NOT a regression.** Explicit goal rule.
  Timing arms still run (to detect *unplanned* shape changes and to report),
  but a slower identical plan never blocks a round.
- **Values gates still bind every round**: TPC-H digest 24/24 MATCH,
  TPC-DS SF0.5 sweep PASS=95 all-zero. A values break stops the round.
- **Parity criterion** (revised by R2): `scripts/pg-plan-parity-diff.py`
  on BOTH corpora, against a **live** PG capture taken with
  `r2-instrument/capture-tpch.sh` / `capture-tpcds.sh` (GUCs pinned in
  session). Verdicts: match / shapediff / unparsed / missingnode /
  error. `scripts/tpcds-plan-diff.py` is byte-equality and is a
  goopg-vs-goopg movement detector only — never a parity criterion.
  **The goal's proof requires unparsed = 0** (else the tool is
  declining to answer) and match = every planneable query.
  Do NOT diff against `bench/tpch/plans-pg/` — it is stale and serial
  (K9).
- Never `git add -A` (foreign WIP lives in the main tree); stage by
  explicit pathspec. Never `gofmt -w` wholesale (repo baseline go1.25).
- `./postgres/` is a READ-ONLY oracle. Bench ports: PG TPC-H :65432,
  goopg TPC-H :65433, goopg TPC-DS SF0.5 :65437, PG TPC-DS :65438.
  Throwaway servers on 55xx; private data clones; cgroup cap via
  `scripts/goopg-test-run.sh`; never `pkill -f goopg`.

## Knowledge base (append as verified)

- **K1 (verified 2026-09-08, in-tree + oracle).** The §3 thesis holds:
  `costindex.go:229-247` charges `cpuTupleCost × tuplesFetched` with NO
  qpqual term and says so itself (names the follow-up: own digest +
  timing). PG: `cost_qual_eval(&qpqual_cost, qpquals)` →
  `cpu_per_tuple = cpu_tuple_cost + qpqual.per_tuple` on `tuples_fetched`
  + startup (`postgres/.../path/costsize.c:806-830`), `qpquals` = quals
  not satisfied by the index. Seq rival: `costSeqscan` charges
  `(cpuTupleCost + cpuOperatorCost × numQualOps) × relTuples`
  (`cost_funcs.go:192-196`). Asymmetry favours index paths.
- **K2 (verified 2026-09-08, in-tree).** `baseSeqScanCostInputs`
  (`joinsearch.go:480`) returns `numQualOps = 0` for any non-`*SeqScan`
  leaf — an index leaf is priced with no qual charge on post-restriction
  rows (§3 second-order hole confirmed).
- **K3 (tooling).** Parity: `scripts/pg-plan-parity-diff.py`,
  `scripts/tpcds-plan-diff.py`, `make plan-gate`, pins in
  `plan_snapshots/`. Prior reading: match=6/shapediff=14 over 20 TPC-H
  queries (2026-09-08, on `fix-parallel-worker-bug` post-#114).
- **K5 (measurement, verified 2026-09-08 the hard way).** `launch.sh`
  decides readiness with `pg_isready`, which a SURVIVING older server on
  the same port answers — the new instance never binds, the launcher
  prints READY, and the arm silently measures the PREVIOUS binary. It
  cost R1 one full pair of captures. **Every arm must use
  `r1-qpqual-index/launch-verified.sh`**, which proves the listener's
  `/proc/<pid>/exe` inode is the binary we built and refuses otherwise.
  Never trust `ps aux | grep goopg` to tell you a port is free.
- **K6 (verified 2026-09-08, instrumented).** The base-rel seed is a
  `PathPrebuilt` wrapping the **pre-search leaf** (`joinsearch.go:434`),
  priced by `costSeqscan` via `baseSeqScanCostInputs`. When the
  pre-search planner already chose an index, that leaf is an
  `*IndexScan` and gets `numQualOps = 0` + fallback pages (K2). Proof:
  TPC-H Q12's inner `Index Scan ... (cost=0.00..60475.14)` printed
  IDENTICALLY before and after R1 while the Merge Join above it rose by
  exactly the qpqual charge — goopg displays one cost for that node and
  costs the join with another. `cost=0.00` startup is impossible from
  `costIndexScanCore` (it always charges a descent).
- **K7 (verified 2026-09-08, counted).** `pg-plan-parity-diff.py` cannot
  parse the node names both engines print: **56 of 62** TPC-DS
  MISSING-NODE verdicts and **9 of 9** TPC-H ones cite `unknown node
  kind` — `Finalize/Partial {Hash,Group,}Aggregate`, `WindowAgg`, `CTE`,
  `SetOp`, `HashSetOp`, `Merge`. MISSING-NODE therefore does NOT mean
  goopg omitted a node. **The instrument cannot currently prove the
  goal's success condition**, so it is fixed first (R2). Teaching it
  names both engines emit forces nothing to be equal.
- **K8 (tooling gap closed 2026-09-08).** R0 verdicted TPC-DS by
  BYTE equality (`tpcds-plan-diff.py`), which can never match across
  engines because costs differ. The comparable channel is
  `pg-plan-parity-diff.py` after normalising `===== Qn =====` to
  `=== Qn`. Both R0 and R1 now have shape verdicts
  (`r1-qpqual-index/tpcds-shape-diff*.txt`).
- **K9 (verified 2026-09-08 — invalidates every R0/R1 parity number).**
  The TPC-H parity target `bench/tpch/plans-pg/` is a **stale, SERIAL**
  capture. Live PG on :65432 (`max_parallel_workers_per_gather=4`,
  read from `pg_settings`) plans TPC-H in PARALLEL. All nine TPC-H
  MISSING-NODE verdicts were this artefact; the true count is 0, and
  Q6 is a clean MATCH. **Never use that fixture as a parity target** —
  capture PG live with `r2-instrument/capture-tpch.sh`. R0 had a live
  capture and diffed the fixture anyway.
- **K10 (verified 2026-09-08, read from both engines).** The TPC-DS
  pair was configured 128x apart: goopg SF0.5 clone `work_mem=512MB`,
  PG :65438 `work_mem=4MB`. `work_mem` decides hash-vs-sort and
  HashAggregate-vs-GroupAggregate, so that comparison measured
  configuration, not planning. Both capture scripts now pin
  `work_mem=64MB` + `max_parallel_workers_per_gather=4` IN SESSION.
  **A comparison's REFERENCE needs the same provenance check as its
  subject** (the K5 discipline, applied to data).
- **K11 (adjudicated 2026-09-08, four systematic causes).** From
  reading 10 plan pairs by hand:
  (a) ~~goopg's planner never sets `AggStrategySorted`~~ **WRONG —
  corrected by R3 §0.** The planner DOES set it, and
  `addGroupingPaths` builds a plain Sort-then-GroupAggregate candidate
  on every grouped query; the `operators_explain.go` comment saying
  otherwise is stale. The real fact is a COSTING inversion: over
  TPC-DS goopg emits `GroupAggregate` 1x / `HashAggregate` 133x where
  PG emits 100x / 29x, because `costAgg` has **no spill arm** and so
  prices the hash table as if memory were infinite (its own comment
  says so). Cause, not symptom, is R3. **Error class: believing a
  comment about what the code does instead of checking — same class as
  the root-causes rev-1 error, see K4.**
  (b) ~~worker count is not computed~~ **WRONG — corrected by R4 §0.**
  goopg DOES implement `compute_parallel_worker`
  (`considerparallel.go:588`). See K14 for the real cause. (Third
  falsified claim of mine in this workstream, all one shape:
  concluding about behaviour from reading instead of measuring.);
  (c) **no parallel-aware hash join** — PG emits `Parallel Hash Join`
  /`Parallel Hash`, goopg plain `Hash Join` under a Gather;
  (d) **Sort/Group keys render as output aliases**, PG renders source
  expressions — TPC-H Q9's tree MATCHES and fails on this alone.
- **K12 (measured 2026-09-08 — the dominant aggregation cause).**
  PG picks `GroupAggregate` mostly because it **delivers an ordering
  something above needs**, not because the hash spills. Evidence: on
  TPC-DS Q81 and Q12 the group counts are 146/351 and 4572/41 — all
  fit trivially in 64MB, so NEITHER engine spills, yet PG sorts and
  goopg hashes. Q12's shape shows the mechanism: PG runs
  `WindowAgg -> Sort -> GroupAggregate` because the window's
  `PARTITION BY` needs the order, so the Sort is owed anyway and the
  sorted aggregate is nearly free. goopg emits `WindowAgg ->
  HashAggregate` with NO Sort — its `WindowAgg` orders internally, so
  the requirement never reaches the planner and no path is ever
  credited for satisfying it. **goopg's upper planner does not model
  ordering requirements**, which is why the sorted aggregate cannot win
  a contest it should. This is the single largest lever left on TPC-DS
  aggregation.
- **K13 (limitation introduced by R3, filed not erased).**
  `partialAggNotionalRows` substitutes a NOTIONAL row count when goopg
  cannot see a real one; with a memory threshold in `costAgg` that
  notional value can now land on the wrong side of it and flip a
  verdict a real row count would not. Needs a real row count
  (`TableStats.RowCount` is not restored at startup — ledger pq-P6),
  not a cost tweak.
- **K14 (measured 2026-09-08 — a parity floor OUTSIDE the planner).**
  goopg's heap stores the same rows in a different number of pages than
  PG. TPC-DS `store_sales`: identical `reltuples` (1,439,608) but
  `relpages` **29,761 (goopg) vs 25,928 (PG)** — and PG's
  `compute_parallel_worker` bands are [9216,27648) for 3 workers and
  [27648,82944) for 4, so BOTH engines computed the worker count
  correctly from the page count each was given. Systematic and
  bidirectional, sorted by column type: `inventory` (all `integer`)
  matches to **0.2%**, numeric-bearing tables run 1.05-1.15x LARGER in
  goopg, and the two `character(N)` tables run 0.57-0.71x SMALLER.
  **R5 resolved both hypotheses**: `character(N)` blank-padding is
  CONFIRMED divergent (PG pads, goopg does not — explains `item` 0.573
  and `customer` 0.712); `numeric` is FALSIFIED — it matches PG exactly
  at one and five columns, with and without fractional digits, and with
  NULLs. The fact-table 15% has NO representation explanation and is
  reassigned to **heap page FILL on bulk load** (goopg's file is truly
  29,761 pages; PG's truly 25,928 with 0 dead tuples).
  **`relpages` is a planner INPUT** — every page-priced term, the
  Mackert-Lohman estimate, and the parallel-worker thresholds — so a
  15% page error separates otherwise-identical plans and **no planner
  change can close it**. Part of this goal is therefore not planner
  work; it is on-disk representation, and as such a PG-compat defect in
  its own right. Detail: `r4-heap-density/FINDINGS.md`.
- **K15 (investigated 2026-09-08 — the top TPC-H category, and it may
  be a LABEL).** With the corpus now fully parsed, TPC-H's divergence
  categories rank `parallelism=18`, `join-order=18`, `scan-type=14`,
  `sort-strategy=13`, `join-method=12`. **Q14 differs on `parallelism`
  and nothing else** — the closest query to a match after Q6/Q13.
  PG prints `Parallel Hash Join`; goopg prints `Hash Join`. But goopg
  is NOT missing the capability: `parallel_hash_build.go` implements a
  cooperative parallel hash build and states that goopg needs neither
  of PG's two schemes because goroutines share an address space, so the
  table is built once and shared by pointer. The gap is that
  **`optimizer.Join` carries no parallel-awareness field** — only
  `Path.ParallelAware` has it (`joinpathsparallel.go:195`), and
  `createplanjoin.go` merely ASSERTS on it — so the information is lost
  at plan construction and EXPLAIN cannot print it.
  NOT YET VERIFIED: whether Q14's chosen path is actually the
  ParallelAware variant. Plumbing the flag is self-verifying — if the
  label appears, the premise held; if it stays `Hash Join`, the path is
  not parallel-aware and that is the finding. Do NOT assume it
  (K11a/K11b/K14-numeric were all assumed and all wrong).
- **K16 (measured 2026-09-08 — supersedes K15; a MECHANISM difference,
  not a label).** After threading `Path.ParallelAware` onto
  `optimizer.Join` and rendering PG's prefix, goopg still emits
  `Parallel Hash Join` **0** times (PG: 9 TPC-H, 139 TPC-DS). The flag
  is genuinely false for every hash join goopg plans — checked at the
  right site (`createplanjoin.go:551`, the one holding
  `assertParallelAwareJoinIsRunnable`; three Join construction sites
  exist, so patching one of three would have faked this zero).
  **goopg parallelises by STAMPING a Gather over a serial subtree
  (`stampParallelScan`, `createplangather.go:112`, a copy-on-write walk
  over an already-built tree); PG parallelises by building PARTIAL
  PATHS and letting them win.** That is what `parallelism=18` on TPC-H
  really is. Next step per
  [[planner_verify_both_candidates_generated]]: instrument `addPath` to
  learn whether `addPartialHashJoinPath` is never called, called and
  declined, or called and outcompeted — three different fixes.
- **K17 (measured 2026-09-08 — a real bug, found before flipping a
  default).** `GOOPG_GATHER_PATHS=all` **crashes the server** on TPC-DS
  Q5: `assertParallelAwareJoinIsRunnable` fires on a `2/1` =
  `JoinTypeRight`/`JoinAlgoHash` join — "the workers' verdicts are not
  row-local, so the join would silently drop or duplicate rows".
  Cause: **`addPartialHashJoinPath` takes `jt parser.JoinType` and never
  compares it to anything** — there is no jointype test anywhere in
  `joinpathsparallel.go`, so it files whatever direction it is handed,
  including RIGHT. The assertion's own comment claims the producer
  "declines outright for SEMI/ANTI"; that describes code which does not
  exist. Sixth stale-comment finding here, same class as K11a/K15.
  The fail-closed assertion paid for itself the first time its
  "unreachable" branch became reachable. Fix: derive the filter from
  `hashJoinIsPartialCapable` rather than hand-listing, so predicate and
  producer cannot drift again.
- **K18 (measurement artefact, fixed at source 2026-09-08).** Two
  BYTE-IDENTICAL TPC-DS captures diff every time: the three unplannable
  queries (Q36/70/86) echo psql's ERROR text, which contains the
  capture script's `mktemp` path. In R9 this briefly read as "plans
  MOVED" and contradicted a design's central claim. Capture scripts now
  use a fixed `parity-capture-$$.sql` name. **Anything that diffs two
  captures — especially a plan PIN — must not reintroduce a random
  path into the output.**
- **K19 (2026-09-08).** Walkers that switch on node kind must handle
  `*Gather`/`*GatherMerge` or they stop at the ROOT once partial paths
  are admitted and report zero of whatever they count. This produced
  messages like "searched tree has 0 joins" and "an ON qual was
  dropped, which is a cross product" from a planner that was working
  correctly — TPC-H values under the flip are 22/22 byte-identical.
  Fixed in `rfjJoins`, `seamLeafLocalFilters` and **production**
  `boundaryWalkChildren`. **Run the cheap decisive check (values)
  before diagnosing an alarming message**; it is what separated a
  walker gap from a search regression here. **FOUR walkers had it**:
  `rfjJoins`, `seamLeafLocalFilters`, production `boundaryWalkChildren`
  (R11) and `rfjLeafCount` (R12). Apply the rule to TRIAGE ORDER too —
  the failure ranked "most likely a genuine defect" was this bug both
  times it was ranked.
- **K20 (2026-09-08).** `GOOPG_GATHER_PATHS` gates PARTIAL PATHS only,
  **not parallelism**. The post-pass (`stampParallelScan` /
  `MaybeAddGather`) produces a Gather regardless — verified via a test
  whose control arm sets mode `off` explicitly and still got one. So
  there is currently **no setting that yields a serial plan** for such
  a fixture, which breaks any test wanting a serial baseline. Also:
  `scripts/planner-flags.env` records what the default IS, so it must
  be regenerated IN THE SAME COMMIT as a default change, never before.
- **K21 (measured 2026-09-08) — ~~the flip costs a hash join PG
  keeps~~ SUPERSEDED BY R17.** On REAL SF=1 data with the flip on,
  goopg hash-joins the multi-key shape on both equalities with the
  residual as a Join Filter — PG's exact structure, same worker count
  (3), same outer. There is NO nested-loop fallback on the corpus; it
  exists only at the TEST FIXTURE's synthetic cardinalities, which is a
  fixture question, not a parity regression. Original (wrong) text:
  PG hash-joins the multi-key shape on both equalities with the
  residual as a Join Filter (verified on :65432, GUCs pinned). goopg
  under `GOOPG_GATHER_PATHS=all` falls back to a nested loop, so the
  flip **costs a hash join PG keeps** while buying the parallel-join
  mechanism PG uses (R14's Q9). Both measured. R10 DESIGN §7's
  acceptance rule — a category regression must be EXPLAINED, not
  outweighed — applies directly, and this one is not yet explained.
- **K22 (2026-09-08 — a NEW error variant, mine).** R16 wrote its own
  caveat correctly ("the SHAPE question is settled; the THRESHOLD
  question is not") and then, in the same document, asserted a headline
  about the threshold case generalised to the corpus. **The check was
  done; the conclusion outran it.** K4 covers concluding without
  checking — this is concluding PAST a check you already performed.
  Guard: when a report states a limitation, the headline and the ledger
  entry must be re-read against that limitation before committing.
- **K23 (measured 2026-09-08 — the flip's real blocker).** goopg's two
  parallelism mechanisms are NOT interchangeable at the aggregate.
  Under `GOOPG_GATHER_PATHS=all`, `generateUsefulGatherPaths` places
  the Gather at the JOIN level and the aggregate is built above it as
  an ordinary one — bypassing `partialaggpaths.go`, which the POST-PASS
  Gather placement does reach. Result: the flip **loses** the
  `Partial`/`Finalize` split goopg already had and PG emits. Q9: PG
  `Partial HashAggregate`; goopg flip OFF the same (a MATCH on that
  node); goopg flip ON plain `HashAggregate`. This is the entire
  `aggregation-strategy` 10 -> 14 move, and it is a WIRING gap, not a
  costing error.
- **K24 (2026-09-08 — ONE root cause, two symptoms).** The upper
  planner receives a **finished `Node`** from the join search, not the
  join rel's paths. `partialaggupper.go`'s own header says it:
  *"upstream seeds `partially_grouped_rel` from
  `input_rel->partial_pathlist`, and the rel carrying `PartialPathlist`
  DIES inside `planJoinlistSearch` before the aggregate stage runs"* —
  the same seam `windowsetoppaths.go:19` names for pathkeys. So:
  **K23** (no partial aggregation over the flip's Gather) needs the
  join rel's `PartialPathlist`; **K12(B)** (sorted aggregate never wins
  under a WindowAgg) needs its `Pathkeys`. Same fix, and they must NOT
  be scheduled as independent rounds. This is the largest single item
  in the workstream and the flip's true prerequisite.
- **K25 (2026-09-09).** `searchedTree` now carries a `*RelOptInfo`, so
  **any generic/reflective plan walker has a path into the entire
  search path graph**. `planFingerprint` (deliberately reflective)
  descended into it and panicked on `reflect.Value.Interface` for
  unexported fields. Two rules, both matching precedent already in that
  file: treat `*RelOptInfo` as a LEAF (as `*catalog.Table` already is —
  "not plan structure, recursing walks the whole schema"), and guard
  `Interface()` with `CanInterface()` (`searchRel` is the first
  unexported POINTER such walks reach; `searchPathkeys` is a slice and
  never took that branch). Copying, serialisation and deep-equal will
  hit this too.
- **K27 (verified 2026-09-09, live write+restart+read).** Correlation
  PERSISTS across restart at HEAD — R23's stated blocker is stale.
  `ANALYZE store_sales` on a private SF0.5 clone wrote slot 3
  (`ss_sold_date_sk` 0.14367048); after stop+restart the same session
  reads back byte-identical values. Write path
  (`pg18_user_catalog_rows.go` slot-3 writer), decode
  (`codec.go:DecodePGStatisticPhysicalRow` stanumbers3 arm) and restore
  (`open.go` `Correlation: float64(sr.Correlation)`) all confirmed live,
  not by reading. What is TRUE underneath: the bench heaps predate the
  slot-3 writer, so their restored stats have no correlation slot
  (TPC-DS `pg_stats.correlation` empty where n_distinct is present) —
  an OPERATIONAL gap (re-ANALYZE), not a code gap.   NOT done here:
  re-ANALYZEing shared bench clusters would mutate peer measurement
  state. TPC-H lineitem.l_orderkey reads -0.0018 post-restore, which is
  the computed value on unordered HammerDB input, not a defect signal.
- **K27 (2026-09-09).** A `seam-decline` is a PARITY signal, not just a
  perf one: a query the PG-shaped search declines falls to the legacy
  path and **cannot converge on PG's plan by any amount of costing
  work**. Watch `GOOPG_PGSHAPED_DP_TRACE=1 | grep seam-decline` when a
  query looks unreachable. Concretely: an `OuterColumnRef` anywhere in
  a CTE body made `planHasEscapingOuterRef` report an escaping ref for
  the CTEScan LEAF, so `chainCarriesLateral` declined the whole
  enclosing join (TPC-DS Q30: cross product instead of PG's index-scan
  inners, 3s -> >300s). `walkPlanExprs` flattens, so depth increments
  only for subquery-bearing EXPRESSIONS — never for a lateral join's
  right side — which is why a ref BOUND inside a subtree reads as
  escaping it.
- **K28 (2026-09-09).** `reduceOuterJoins` runs AFTER `planFromClause`
  builds the node tree, so its in-place demotion of `s.FromExprs`
  reaches only the SJI deconstruction — **it has never driven a plan.**
  Consequences: (a) the plan keeps `JoinTypeLeft` while
  `join_info_list` says there is no outer join, which trips the
  fail-closed `outerLinksHaveSJInfos` guard and declines the statement
  (K27 — 7 of 13 TPC-DS declines); (b) PG's Q49 has NO outer join
  (6 Nested Loop) where goopg has 3 `Hash Left Join`, a direct parity
  divergence. **Moving the call is NOT the fix**: measured, it breaks
  6 tests including 2 on VALUES (a WHERE qual on a RIGHT JOIN's
  nullable arm lands below the join that produces the NULLs). The
  demotion logic must be audited as a PLAN REWRITE against PG's
  `reduce_outer_joins` first — classic "dead code is not a reference
  implementation".
- **K29 (2026-09-09 — the decline is LOAD-BEARING).** goopg's
  `applyDemotion` produces WRONG verdicts, not merely unverified ones
  (K28 understated it). `accumulatedNN` accumulates ON-clause
  strictness from INNER joins BELOW an outer join, so the RIGHT arm
  reads its own nullable side as non-nullable and demotes RIGHT->INNER
  where PG does not — measured, it returns 0 rows where 1 is correct
  (`WHERE rj_a.id IS NULL` over a RIGHT JOIN). PG's
  `reduce_outer_joins_pass2` only lets quals from ABOVE constrain a
  join. **The `outerLinksHaveSJInfos` decline is the only thing
  containing this**: the bad verdict reaches `join_info_list` but not
  the plan, and the resulting disagreement declines the statement.
  ~~Retiring the decline without first fixing the propagation ships
  wrong rows.~~ **STEP ONE DONE (R27 REPORT.md):** both the RIGHT and
  FULL arms now judge their nullable side against `upperNN` (quals from
  ABOVE), PG's rule. Oracle-verified: PG emits `Merge LEFT Join` and
  returns BOTH rows for `ra join rb on … right join rc on …`; goopg had
  demoted to INNER. Three optimizer tests pinned the bug and are
  corrected. Gates: 5 executor VALUES tests now PASS, TPC-H values
  byte-identical, sweep PASS=95 all-zero. **The decline no longer masks
  anything**, so R27 §4a's ordering transplant can proceed on its own
  merits. Remaining: the ordering, then the declines.
- **K30 (2026-09-09).** goopg's **LEFT->ANTI demotion is not
  plan-complete.** PG's conversion also DROPS the `IS NULL` qual that
  forced it, because an anti-join's output has no nullable-side column
  for that qual to test. goopg changes the join type and leaves the
  qual, so `LEFT JOIN … WHERE p.y IS NULL` filters every surviving row
  — measured, **0 rows where 1 is correct**. Only the INNER verdict is
  therefore transplanted to the plan (`demotedForPlan`). Completing the
  ANTI conversion (drop the forcing qual) is the prerequisite for
  transplanting it. Same family as K29: a demotion path that never
  drove a plan was never completed.
- **K31 (2026-09-09).** goopg **materialises** CTEs where PG **inlines**
  them. PG 12+ inlines a non-recursive CTE referenced once
  (`inline_cte`, subselect.c; `NOT MATERIALIZED` is the default), so the
  `CTE Scan` node does not exist and the underlying tables enter the
  planner with real statistics. TPC-DS: **goopg 111 `CTE Scan` nodes,
  PG 68.** goopg's `pushQualsThroughSingleRefCTEs` is explicitly the
  QUAL-PUSHDOWN half of that composition — it carries the restriction
  into the body and leaves the node. Consequences: the
  `outer-over-derived` firewall fires on those derived inputs (3
  declines); their rows are synthesised where PG has real stats (the
  goal's "same statistics" premise); and the search sees one opaque rel
  instead of the tables inside it. **CENSUS DONE**: of 63 CTE
  declarations across 30 TPC-DS queries, **40 (63%) are
  single-reference** and PG inlines them; 23 are multi-reference and PG
  materialises those too. goopg materialises all 63. So the eligible
  target is **40 declarations** (the 43-node figure counts SCAN NODES —
  different unit, do not conflate).
- **K4 (rev-1 error pattern, from §6).** Never conclude from a file
  without checking its callers (`pathgen.go`/`generateScanPaths` is
  test-only; production seed is `newPrebuiltPath`). Every design must
  cite call sites, not files.

## READ FIRST (0): HOW we measure

`METHODOLOGY.md` — the measurement pipeline, the gates, the server
traps, the diagnosis order, and why rounds are judged by CATEGORY
rather than by match count. Working copies of every script it names are
in `methodology/`. Read it before running anything; several of its
rules exist because breaking them silently invalidated an arm (K5, K9,
K10, K18).

## READ FIRST (2): which queries are even ELIGIBLE

`r26-seam-decline-audit/FINDINGS.md` (2026-09-09), acting on K27.
A query the PG-shaped search DECLINES falls to the legacy path and
**cannot converge on PG's plan by any costing work**.

- **TPC-H: 0 declines.** Every query is admitted, so TPC-H's 2/22 is
  ENTIRELY costing/candidates. Seam work cannot help TPC-H.
- **TPC-DS: 9 declines across 5 queries** (was 13/7; R27 admitted
  **Q49 and Q93**). Q51, Q68, Q77, Q78, Q97. Reasons now:
  `outer-over-derived` 3 (Q77 x2, Q78), `outer-spine` 2 (Q51, Q97),
  `outer-link-no-sjinfo` 1 (Q78), `lateral` 1 (Q68).
  **The surviving `outer-link-no-sjinfo` is a DIFFERENT cause** from
  Q49's — re-diagnose, do not extend R27.

These 7 are a HARD FLOOR: unlike the other 92 they are not merely
outcosted, they never enter the search. Sequencing therefore has two
axes, not one — how many queries a category blocks, AND whether a query
is eligible at all.

## READ FIRST: what "all plans match" requires

`ROADMAP-to-all-match.md` (2026-09-09). **It is a CONJUNCTION, not a
sequence.** No query has a single divergence — every non-matching query
differs from PG in 4-7 categories at once, and **zero** queries are
blocked by join-order alone. So **no single fix flips any query to
MATCH**, and the match count will stay near zero until nearly all
category work is done. Judge a round by its CATEGORY, not the match
count.

Queries blocked, by category (TPC-DS of 99 / TPC-H of 22):
join-order **95/17**, parallelism 89/16, aggregation-strategy 81/10,
sort-strategy 79/13, join-method 72/12, scan-type 72/14,
parameterisation 42/6, rendering 35/7, qual-placement 13/6.

**K26 (2026-09-09): join-order's cause is now MEASURED** — see
`K26-join-order-implied-equalities.md`. goopg's DP declines
`{part}|{partsupp}` on TPC-H Q9 with `reason=no-join-clause` (20 such
declines), because Q9's clauses all run through `lineitem` and those
two rels share no DIRECT clause. PG joins them via an EQUIVALENCE
CLASS: both are equated to `l_partkey`, so
`generate_join_implied_equalities` (equivclass.c) synthesises
`p_partkey = ps_partkey`. goopg HAS equivalence classes
(`equiv_class.go`) but the seam gives the search only "the equivalence
class's CONSTANTS", never derived JOIN CLAUSES — **deliberately, and
measured**: the seam records that adding the transitive `a = c` "would
hand the search new JOIN clauses and reshape plans broadly — measured:
it broke the pinned-semi-join layout
`TestPreDPPinnedSemiKeysResolveAfterDP` asserts ... That half stays on
its legacy caller pending its own evaluation."
    **OBSTACLE READ (K26 §6)**: that test is a **REMAP** test, not a
    layout test — "every ColumnRef in the semi join's keys/predicate
    must resolve ... in the post-DP outer schema" — and its fixture
    (`b1_j = b2_j AND b2_j = s3_j`) is exactly an equivalence class
    whose closure enables MORE reordering. So the derived equality is
    not wrong; the pinned semi join's F8 REMAP is incomplete for the
    wider layouts it makes reachable. **The round's work is in
    `predp`'s remap, not in `equiv_class.go`.**
    **MEASURED 2026-09-09 with the transitive half ENABLED (K26 §7)**:
    it breaks **3 tests**, not a corpus — far smaller than the old
    note's "reshapes plans broadly". And the named obstacle fails by
    NIL-DEREF INSIDE ITS OWN HELPER (`findSpineSemi` returns nil at the
    first non-semi Join; caller does not check) — a planner-shaped
    symptom from a test walker, K19's fourth family. Descending joins
    is NOT enough: the semi join is still not found, so it is not
    merely relocated. **ANSWERED (K26 §8)**: the semi join is NOT lost — it changes FORM to
    a `*NestedLoopIndexJoin` (still `JoinTypeSemi`, still on the spine).
    The test handles the NLI branch; the nil-deref is ONE expression in
    it — `nliIn(nli.Inner).Key`, where `nliIn` returns nil for the
    inner shape implied equalities produce. **So the next step is: what
    is `nli.Inner`, and should `nliIn` recognise it?** A bounded
    question about one helper — NOT the F8 remap (§6's hypothesis is
    dead) and NOT evidence that implied equalities are wrong. So the mechanism
    EXISTS
(re-wiring, not a port), the round's first obstacle is NAMED in
advance, and the deferral's reason — "reshapes plans broadly" — is
precisely what THIS goal's rule permits. That single gap makes
goopg's reachable join orders a strict subset of PG's on any
star-shaped query — most of TPC-H, essentially all of TPC-DS. It is
CANDIDATE GENERATION, so no cost work can reach it.

**join-order is the dominant blocker and is UNTOUCHED** — it is the
join search reproducing PG's `join_search_one_level`, larger than
anything attempted so far.

## Rounds

- [x] **R0 — baseline (captured 2026-09-08).** Evidence:
  `r0-baseline/` (4 captures + 2 diff outputs).
  - Method: private clones (`/tmp/parity-r0/{tpch,ds05}`, cp -a of the
    bench clusters) + private binary `tmp/goopg-parity-r0` (built at
    `9edf01adc`) on :5543/:5544 via `scripts/goopg-test-run.sh`
    (GOOPG_ANALYZE_SEED=20260905, GOMEMLIMIT=12GiB, GOGC=off); the shared
    :65433 server was left untouched (started 17:05 by an unknown peer —
    displacing it is the documented collision hazard). Plain EXPLAIN, one
    `===== Q<n> =====` section per query, per-statement EXPLAIN-prefix
    split exactly as `sf05_capture_plans` does; TPC-H texts from
    `internal/testutil/tpch/tpch.go` Queries() (Q15 = CREATE VIEW kept in
    the raw capture, dropped for the diff; Q15a-VIEWBODY via
    `Q15ViewBody()`), TPC-DS from `query1..99.sql`. PG references live on
    :65432 (tpch) / :65438 (ryo@tpcds05). All foreground, per-query
    `timeout 120`, failures recorded inline, none fatal.
  - **TPC-H: MATCH=1 (Q13 only), SHAPE-DIFF=12, MISSING-NODE=9 over 22**
    (`pg-plan-parity-diff.py` vs `bench/tpch/plans-pg/`; sections
    Q1–Q14, Q15a-VIEWBODY, Q16–Q22 — the Q15 CREATE VIEW has no plan
    shape and Q15b-MAIN has no PG fixture, so both are out of parity
    scope on both sides). Caveat: 9 MISSING-NODE verdicts are partly
    tool blindness — the comparator does not know `Finalize/Partial
    HashAggregate` ("unknown node kind"), so MISSING-NODE ≠ proven real
    divergence; adjudicate per query in later rounds. (Prior reading
    match=6/14 was on another branch/commit.)
  - **TPC-DS: byte-same=3/99, changed=96** (`tpcds-plan-diff.py`,
    byte-for-byte so cost/rows drift counts). The 3 sames are the
    Q36/70/86 error blocks — identical parse failures on BOTH engines
    (the known PG_SKIP dsqgen artefacts) — so **0/96 real plan matches**.
    Unplannable on both engines: 36, 70, 86 (out of scope for the proof
    by definition: PG itself produces no plan).
  - Servers left RUNNING (:5543 TPC-H, :5544 DS05) for R1+.
- [x] **R1 — qpqual on the index path** (root-causes §7.1) — DONE
  2026-09-08. Report: `r1-qpqual-index/REPORT.md`. Landed at all five
  index-cost sites; a review of the implementation caught the
  parameterised site SUBTRACTING a join-clause count from a
  local-restriction count (disjoint populations; yields -1 and CREDITS
  the index path) — fixed as `paramIndexQualOpCount`, pinned.
  **Result: values green both corpora (TPC-H 22/22 identical; TPC-DS
  PASS=95 all-zero), 1 TPC-H plan and 33 TPC-DS plans repriced, and
  parity moved by ZERO on both** (TPC-H 1/12/9 unchanged; TPC-DS
  0/34/62 unchanged). The charge is not inert — Q12 rose by exactly
  5 x cpu_operator_cost x 6,001,255 — it just never changes which path
  wins. Yielded K5/K6/K7/K8. First capture pair was discarded: it
  measured the R0 binary (K5).
  ORIGINAL SCOPE, for the record: Charge PG's
  `(cpu_tuple_cost + qpqual) × tuples_fetched` (+ startup) for
  non-index-satisfied quals at all five index-cost sites. Design must
  define WHERE the qual list comes from per site (no new candidates).
  Gates: optimizer/executor suites, values both suites, parity A/B both
  corpora (expect Q12-class moves toward PG), timing table reported
  (not adjudicated unless shapes move unexpectedly).
- [x] **R2 — make the instrument able to prove the goal** (K7/K8) —
  DONE 2026-09-08. Report: `r2-instrument/REPORT.md`. UNPARSED is now 0
  on both corpora, and validating the instrument found the parity
  TARGET was wrong twice over (K9 stale serial TPC-H fixture, K10
  128x work_mem gap on TPC-DS). **Every R0/R1 parity number is
  superseded.** Corrected baseline: **TPC-H match=2 shapediff=20
  unparsed=0 missingnode=0**; **TPC-DS match=0 shapediff=72
  unparsed=0 missingnode=24 error=3**. TPC-H Q6 had been planning
  identically to PG for two rounds while filed as MISSING-NODE. The
  design's advance prediction (reclassification goes to SHAPE-DIFF, not
  MATCH) held exactly: zero queries moved to MATCH from the tool change.
  Yielded K9/K10/K11. ORIGINAL SCOPE: Teach `pg-plan-parity-diff.py` the
  node names both engines already emit (`Finalize/Partial` aggregates,
  `WindowAgg`, `CTE`, `SetOp`/`HashSetOp`, `Merge`) and make the TPC-DS
  corpus a first-class channel (section normalisation, not byte
  equality). This changes NO plan: it can only reclassify a verdict the
  tool was guessing at, and every reclassification must be adjudicated
  by hand against the two plan texts before it is believed. Without it
  the goal's success condition is unmeasurable. Gate: the tool's own
  test (`scripts/pg-plan-parity-diff-test.py`) plus a hand-adjudicated
  sample of at least 5 reclassified queries per corpus.
- [x] **R3 — the memory-blind HashAggregate** — DONE 2026-09-08.
  Report: `r3-hashagg-spill/REPORT.md`. PG's spill arm transcribed
  faithfully; TPC-DS `GroupAggregate` 1 -> 13 (PG: 100), TPC-H
  unmoved, parity verdicts unchanged on both, values green on both.
  The old objection did NOT reproduce (TPC-H did not move at all).
  **The arm is a minor contributor — see K12 for the dominant cause it
  exposed.** Broke `TestPartialAggVerdictIsScaleFree` legitimately (a
  memory threshold is not scale-free, and PG's model is not either);
  bounded the property to the sub-threshold regime + added a companion
  pin. ORIGINAL SCOPE:
  design `r3-hashagg-spill/DESIGN.md`). `costAgg` has no spill arm, so
  the hashed candidate is priced as if memory were infinite and beats
  its sorted rival (which always pays a Sort) on every large grouping —
  exactly the population where PG spills and picks `GroupAggregate`.
  Transcribe PG's arm (`costsize.c:2783-2840`, `hash_agg_entry_size` /
  `hash_agg_set_limits`). It is provably INERT below the memory
  threshold, so small groupings cannot move. Re-opens a standing
  objection whose timing leg the goal rule voids and whose parity leg
  was measured against the invalid references (K9/K10).
- [ ] **R4 — model the ordering requirement** (K12, NEW, largest
  remaining lever). PG's upper planner selects the cheapest path that
  SATISFIES a required ordering, so a sorted aggregate that delivers
  the order a WindowAgg / ORDER BY / DISTINCT needs wins a contest the
  hashed one cannot enter. goopg's WindowAgg orders internally, so the
  requirement never reaches the planner. Oracle:
  `create_grouping_paths` pathkey handling +
  `get_cheapest_fractional_path_for_pathkeys`.
- [x] **R4 — heap density** (was "compute the worker count"; premise
  K11b falsified) — DONE 2026-09-08, findings only, no code change.
  `r4-heap-density/FINDINGS.md`. goopg's worker rule is correct; it is
  fed a page count 14.8% larger than PG's for identical rows. See K14.
- [x] **R5 — per-type storage sizes** (K14 follow-up) — DONE
  2026-09-08, findings only. `r5-per-type-density/FINDINGS.md`.
  9 probe tables on both engines: everything matches EXCEPT
  `character(N)`, which PG blank-pads and goopg does not. `numeric`
  falsified. Fact-table gap reassigned to page fill.
- [x] **R6 — the window's Sort belongs in the plan** (K12 slice A) —
  DONE 2026-09-08. `r6-window-sort/REPORT.md`. Planner stacks PG's
  `create_one_window_path` Sort; executor honours a fail-closed
  `Presorted` flag. TPC-DS gains 13 Sorts below WindowAgg (PG has 6);
  `GroupAggregate` unchanged at 13 and parity unchanged on both corpora
  — **all four advance predictions held, including the negative one**.
  Values green on both. The residual 13-vs-6 is now a MEASUREMENT of
  what slice (B) is worth; it was unobservable before.
- [x] **R7 — carry parallel-awareness onto the plan node** (K15) —
  DONE 2026-09-08. `r7-parallel-aware-label/REPORT.md`. Plumbing landed
  and is correct; **the premise was FALSIFIED by the measurement it was
  designed to make** — goopg emits `Parallel Hash Join` ZERO times on
  either corpus (PG: 9 on TPC-H, 139 on TPC-DS). No parity movement.
  See K16 for what the category actually is. ORIGINAL SCOPE:
  Add the field to `optimizer.Join`, set it from `Path.ParallelAware`
  in `createplanjoin.go`, and render `Parallel Hash Join` as PG does.
  Accurate rather than cosmetic — goopg really does build the hash
  cooperatively. Cheapest identified round with a concrete target
  (Q14 -> MATCH would be +1 on TPC-H) and it is self-verifying.
  Same class as R2's `WindowAgg` label fix.
- [x] **R8 — why no partial hash-join path ever wins** (K16) — DONE
  2026-09-08, findings only. `r8-partial-path-admission/FINDINGS.md`.
  Answer: the CONSUMER is off. `GOOPG_GATHER_PATHS` defaults to off, so
  `generateUsefulGatherPaths` reads nothing and every partial path is
  discarded. The knob was parked on a TIMING decision (D-05: -10..22%
  TPC-H) that **this goal's rule voids**. Probed with `=all`:
  `Parallel Hash Join` 0 -> 19 on TPC-H (PG 9) and 0 -> **132** on
  TPC-DS (PG 139); TPC-H `parallelism` 18 -> 15, `join-method` 12 -> 11,
  `qual-placement` 7 -> 5, but `aggregation-strategy` 10 -> 14, match
  unchanged. **BLOCKED by K17.** Three
  candidate causes, one instrumentation step to distinguish them. This
  is TPC-H's joint-top divergence category and it is a mechanism gap,
  not a label.
- [x] **R9 — jointype filter on the partial hash-join producer** (K17)
  — DONE 2026-09-08. `r9-partial-jointype-filter/REPORT.md`.
  `partialHashJoinTypeOK` admits {INNER, LEFT, SEMI, ANTI}, pinned
  BY TEST against `hashJoinIsPartialCapable` for all 7 jointypes (K17
  was a class bug — a comment drifting from code — so a second
  hand-written list would reproduce it). R8's crash reproduction now
  plans. Default-off byte-identical on both corpora; values green both.
  **R10 unblocked.** ORIGINAL SCOPE: Derive from `hashJoinIsPartialCapable`; unit pin per
  jointype; correct the stale comment. Prerequisite for R10.
- [~] **R10 — flip `GOOPG_GATHER_PATHS`** — ATTEMPTED AND REVERTED
  2026-09-08, nothing shipped, tree green.
  `r10-gather-paths-default/REPORT.md`. The one-line change is correct
  and its justification stands (PG has no Gather post-pass), but the
  flip fails **15 tests** pinning the pre-flip world
  (`failing-tests-under-the-flip.txt`) in at least three distinct
  classes — explicit stage pins, seam shape pins, and a generated
  provenance artefact — including
  `TestC19fPathModelGatherExecutesAsAParallelHashJoin`, the test written
  FOR this mechanism. Bulk-updating them to make the change land is the
  failure mode this workstream exists to avoid, so it was reverted.
  ORIGINAL SCOPE: Full
  values gates both corpora; adjudicate every moved plan; explain the
  `aggregation-strategy` 10 -> 14 move before accepting.
- [x] **R11 — adjudicate R10's 15 tests** — DONE 2026-09-08.
  `r11-adjudicate-gather-walkers/REPORT.md`. **15 -> 8.** Seven were one
  bug in three walkers that cannot descend a `Gather` — including
  `boundaryWalkChildren`, which is PRODUCTION code whose own contract
  says it enumerates "every kind that can sit between a statement's
  root and a spliced searched subtree". Fixes landed and green at the
  shipping default. R10's alarming messages ("0 joins", "an ON qual was
  dropped, which is a cross product") were walker blindness: TPC-H
  values under the flip are 22/22 byte-identical, which was checked
  BEFORE diagnosing. 8 remain (2 stage pins, 1 generated artefact,
  5 needing real judgement).
- [x] **R12 — the remaining 8** — DONE 2026-09-08.
  `r12-remaining-eight/REPORT.md`. **8 -> 7.** The one R11 called "the
  most likely genuine defect" (outer-join null extension) was **K19 a
  fourth time** — `rfjLeafCount` counted a Gather-containing 3-leaf side
  as 1 leaf. Second round running where the alarming message was the
  walker, and the second time I ranked it as the likeliest real bug.
  Remaining 7: 2 stage pins + 1 generated artefact (all mechanical),
  and **4 showing REAL plan movement** — the Slice3 pair changed
  character once the walkers could see (now "unexpected/missing narrow
  build", i.e. a different build side), plus
  `TestSplitEqualityForHashMultiKey` and
  `TestOwnedBuildPoisonPrebuiltBoundary`.
- [x] **R13 — the mechanical three** — DONE 2026-09-08, findings only,
  nothing committed as code (deliberately).
  `r13-mechanical-and-postpass/FINDINGS.md`. The provenance artefact
  regenerates to a one-line change but **must land WITH the flip** —
  it records what the default IS, and committing `unset(all)` while the
  default is `off` would make the stamp bench reports cite a lie.
  `TestC19fPathModelGather...`'s control arm sets mode off EXPLICITLY
  and still got a Gather: the post-pass (`stampParallelScan`) is not
  gated by `GOOPG_GATHER_PATHS` at all (K16's two mechanisms), so no
  setting gives that fixture a serial baseline. Its assertion is
  load-bearing ("any row difference is a wrong answer"), so the fix may
  be an off switch for the post-pass, not a weakened test — a design
  question, not an edit.
- [x] **R14 — adjudicate the first of the 4 against PG** — DONE
  2026-09-08, findings only. `r14-adjudicate-against-pg/FINDINGS.md`.
  Q9 compared three ways: **PG uses `Parallel Hash Join` twice; goopg
  WITH the flip uses it three times; goopg WITHOUT it uses none.** So
  the flip moves the parallel-join mechanism from absent to present —
  the first EVIDENCE for R10's argument, which until now rested on
  principle alone. Join ORDER is unchanged by the flip and still
  differs from PG (that is `join-order`, the other joint-top category),
  so Q9 does not become a MATCH — exactly as R10 DESIGN §6 predicted.
  `TestSlice3LiveQ9ShapeDerivation`'s narrow-build failures are a
  consequence of the new build sides, i.e. a justified re-baseline.
- [x] **R15 — the multi-key item, narrowed** — DONE 2026-09-08,
  findings only. `r15-multikey-adjudication/FINDINGS.md`. The failing
  arm is the COST-driven enumerator, not the shape-capability one
  (`splitEqualityForHash`'s own arm still passes), and the test's
  header documents that a relation the planner cannot size floors at
  one row where "a nested loop is genuinely the cheaper plan". So this
  is **not a lost capability** — it is a cost movement, same family as
  R14's narrow-build changes. NOT settled: whether the price is right,
  which needs PG's answer for the same shape at the same cardinalities
  (synthetic fixture -> run the SQL on :65432, do not read a capture).
- [x] **R16 — PG's verdict on the multi-key shape** — DONE
  2026-09-08, findings only. `r16-pg-multikey-verdict/FINDINGS.md`.
  The fixture is TPC-H-shaped, so it ran on :65432 directly.
  **PG HASH-JOINS on both equalities** (`Hash Cond: (ps_partkey =
  l_partkey AND ps_suppkey = l_suppkey)`) with `ps_availqty > sum` as a
  Join Filter. So goopg's nested-loop fallback under the flip is a
  **confirmed divergence from PG**, not a justified re-baseline — the
  opposite disposition from R14's Slice3 finding. **The flip is
  therefore not unambiguously good**: it buys PG's parallel-join
  mechanism (R14) and costs a hash join PG keeps. Caveat recorded: the
  fixture's own row counts are synthetic and smaller; the SHAPE
  question is settled, the THRESHOLD question is not.
- [x] **R17 — the multi-key shape on REAL data** — DONE 2026-09-08.
  `r17-multikey-on-real-data/FINDINGS.md`. **Corrects R16/K21.** Under
  the flip goopg produces `Gather(3) -> Parallel Hash Join` with
  `Hash Cond: (ps_partkey = l_partkey AND ps_suppkey = l_suppkey)` and
  `Join Filter: (ps_availqty > s)` over `Parallel Seq Scan on partsupp`
  — **PG's structure, four of seven rows identical including worker
  count**. The closest goopg has come to a PG plan on a non-trivial
  shape here. The nested loop exists only at the fixture's synthetic
  counts. R16 stated this caveat and then reasoned past it in its own
  headline — see K22.
- [ ] **R18 — the fixture question**: does the synthetic multi-key
  fixture still test what it means under the flip, or do its counts now
  sit on the wrong side of the one-row floor? If the latter, fix the
  FIXTURE, not the planner.
- [x] **R19 — `aggregation-strategy` 10 -> 14 EXPLAINED** — DONE
  2026-09-08. `r19-aggregation-strategy/FINDINGS.md`. Re-measured
  post-R9 (survives unchanged). Exactly four queries gain it — Q5, Q9,
  Q12, Q19 — and **every one also loses a category** (Q19 goes 5 -> 4,
  strictly better); the raw count conceals a trade rather than a pure
  regression. **Cause: under the flip goopg LOSES the Partial/Finalize
  split it already had.** Q9: PG `Partial HashAggregate`, goopg flip
  OFF `Partial HashAggregate` (matches!), goopg flip ON plain
  `HashAggregate`. `generateUsefulGatherPaths` places the Gather at the
  JOIN level and the aggregate is built above it, bypassing
  `partialaggpaths.go` — which the post-pass shape does reach. So it is
  a WIRING gap, not a costing error. R10 DESIGN §7's objection is
  DISCHARGED; recommendation is to fix R20 first so the flip is
  strictly toward PG.
- [x] **R20 — located K23's cause exactly** — DONE 2026-09-08,
  findings only. `r20-partial-agg-over-gather/FINDINGS.md`. One line:
  `addPartialAggSplitPath` (`partialaggupper.go:92`) refuses when
  `subtreeHasGather(child)` — a DELIBERATE coexistence guard (two
  Gathers => every worker reads the whole relation, N+1 copies). Under
  the flip the Gather is at the join level, so the guard fires. It
  cannot simply be relaxed: PG never has a Gather below a partial
  aggregate because it aggregates ON THE PARTIAL PATH first
  (`planner.c:7351`) and gathers after (`:7704`); deleting the guard
  would give a double-Gather, not PG's plan.
  **K23 and K12(B) ARE THE SAME ROOT CAUSE** — see K24.
- [~] **R21 — the upper-planner seam** (K24). Design `3b0322c11`.
  **SLICE 1 LANDED 2026-09-08** (`r21-upper-planner-seam/REPORT-slice1.md`):
  `joinlistRel` gains `rel *RelOptInfo`, set from the chosen path's
  `p.Rel`; `planJoinlistSearch` returns it; `tryPGShapedJoinSearch`
  discards it with an explicit `_` marking slices 2/3. Gate —
  **byte-identical plans on BOTH corpora** — passes; suites green.
  Nothing consumes it yet.
  - [x] **Slice 2a (plumbing) LANDED** 2026-09-08/09
    (`REPORT-slice2a.md`). **The §8 hop table was not needed**: the
    codebase already solved this for `searchPathkeys` by carrying it on
    the `searchedTree` TAG, with a comment saying threading would touch
    fifteen signatures. `searchedTree` now carries
    `searchRel *RelOptInfo`, stamped in `stampSearchPathkeys` (the one
    site holding both the published root and its path). **Zero
    signatures changed outside `searchedtree.go`/`createplanroot.go`.**
    Gate: byte-identical plans BOTH corpora; suites green.
    **K25**: a `*RelOptInfo` on a node is a gateway to the whole path
    graph — every generic plan walker now needs a leaf rule for it.
  - [ ] ~~Slice 2a (plumbing)~~ SUPERSEDED — was: thread the rel
    `tryPGShapedJoinSearch` -> `tryJoinSearch` -> `planSelectWithSettings`
    -> `createGroupingPaths` (which today takes NO rel) and on to
    `addPartialAggSplitPath`. Route crosses `planner.go`; the full hop
    table is in `r21-upper-planner-seam/DESIGN.md` §8. Gate: slice 1's
    — byte-identical plans both corpora, since nothing consumes it.
    `createWindowPaths` needs the same rel for slice 3, so do it once.
  - [x] **Slice 2b PREREQUISITE landed** 2026-09-09
    (`REPORT-slice2b-prereq.md`): `searchedRelOf` accessor, gated
    byte-identical on TPC-H. **The naive version returns nil** — the
    aggregate's child is a `*Project` WRAPPING the search root, caught
    by probing rather than assumed; it now descends via
    `boundaryWalkChildren` (same contract R11 taught about Gather).
    **Measured under the flip: `rel=true partialPaths=1
    hasGather=true`** — the partial path exists and is reachable, so
    K23 is blocked on nothing unknown. The refusal site carries this
    measurement in a comment.
  - [~] **Slice 2b (K23 behaviour)** — WRITTEN, MEASURED, NOT ENABLED
    2026-09-09 (`REPORT-slice2b.md`). **DESIGN §9's plan was wrong**:
    no partial PATH is needed. This file's own blocker-1 note says the
    partial plan IS the Gather's child subtree, so slice 2b is an
    UNWRAP of an existing node — no `createPlanNode`, no coordinate
    translation, no boundary-map hole. `gatherToUnwrapForPartialAgg`
    is committed (narrow: boundary-chain Gather only, never
    `*GatherMerge`, which carries an ordering) with its call site
    COMMENTED OUT.
    **Measured enabled: `aggregation-strategy` 14 -> 10 — K23's
    success test MET** — but TPC-H Q9/Q13 crash:
    `Aggregate input target [] drops group-input column "l_year"`.
    **ATTEMPTS 2 AND 3 CORRECTED THIS, both measured — the guesses
    below were wrong, read the outcome first.**
    - Attempt 2 (clear the stale stamp on a copy of the agg spec):
      SAME PANIC. The target is applied POST-HOC to the emitted node,
      so a spec copy never reaches the assertion.
    - Attempt 3: **`deriveAggregateInputKeep` was RIGHT.** It matches
      child columns to group inputs BY NAME, and Q9 groups on
      `l_year` — a COMPUTED column made by a `Project` ABOVE the
      Gather. My helper walked to the Gather at any depth and returned
      ITS CHILD, discarding that Project, so nothing matched by name.
      The bug was mine, one frame up from where the panic pointed.
    - Narrowed to an IMMEDIATE Gather: all 22 plans build, no crash —
      but `aggregation-strategy` stays 14. **Sound but INERT**: the
      search's Gather always sits behind a Project.
    **ATTEMPT 4 (SPLICE) WORKS — K23 CLOSED.** Under the flip: all 22
    TPC-H plans build (was 20), `unparsed` 2 -> 0,
    `aggregation-strategy` 14 -> **10**, and Q9's aggregate is PG's
    shape exactly (`Finalize HashAggregate -> Gather -> Partial
    HashAggregate`). Values 22/22 byte-identical UNDER THE FLIP, and
    default-off plans+values unchanged. Implementation:
    **SPLICE the Gather out of the chain, keeping every
    wrapper** — `Project(Gather(X))` -> `Project(X)`. A Gather is
    schema-preserving, so the input row's columns (incl. `l_year`) are
    unchanged, which is what the name-matched derivation needs. Needs
    wrapper-cloning machinery. **Do not weaken the assertion** —
    dropping a group-input column silently changes GROUP BY.
    SUPERSEDED GUESS: re-derive the target against the unwrapped child.
    Crash verified MINE, not the flip's (R19 captured all 22 under the
    flip cleanly). ORIGINAL: see DESIGN §9.
    Site: new arm in `addPartialAggSplitPath` before the guard. Seed
    from `searchedRelOf(child).PartialPathlist[0]` (PG's
    `cheapest_partial_path`, planner.c:7452) instead of
    `newPrebuiltPath(partialRel, child)`; the rest of the shape
    (`pseed` -> partial agg -> `nsGather` -> finalise) already exists.
    **TRAP**: a partial path's `Rows` is ALREADY per-worker, so
    `getParallelDivisor` must not be applied twice — that yields wrong
    ROW COUNTS, not an error, which is why the values gates are
    load-bearing for this slice. Was: add an arm BEFORE the guard —
    when `searchedRelOf(child)` has a non-empty `PartialPathlist`,
    build partial agg -> Gather -> finalise from it. Guard stays for
    the post-pass route. Was: partial aggregation from
    `rel.PartialPathlist`, built BELOW the Gather
    (`create_partial_grouping_paths`, planner.c:7351). Success test:
    `aggregation-strategy` 10 -> 14 under the flip disappears.
  - [ ] **Slice 3 (K12 B)**: pathkeys for the ordering contest.
  ORIGINAL SCOPE:
  Give the grouping and window stages the join rel's PATHS
  (`PartialPathlist`, `Pathkeys`) instead of a finished `Node`.
  Unblocks K23 and K12(B) together. Oracle:
  `create_partial_grouping_paths` + `gather_grouping_paths`
  (`planner.c:7351`, `:7704`) and `create_grouping_paths`' pathkey
  handling. Largest single item in this workstream; the flip stays
  reverted until it lands. The
  producer exists and is enabled; find why it does not fire under
  `generateUsefulGatherPaths`' placement. Confirm the candidate is
  GENERATED before theorising about cost
  ([[planner_verify_both_candidates_generated]]). True prerequisite for
  landing the flip. ORIGINAL R19 SCOPE: (R8's probe),
  the last unexplained objection to landing the flip per R10 DESIGN §7.
  ORIGINAL R17 SCOPE: why is the nested loop priced below the hash join under
  the flip?** Instrument `addPath` to confirm the hash candidate is
  generated before theorising about cost terms
  ([[planner_verify_both_candidates_generated]]). Then adjudicate
  `TestSlice3FilterColumnSurvivesNarrowing` and
  `TestOwnedBuildPoisonPrebuiltBoundary`, and decide the flip: either
  fix the nested-loop choice so the flip is strictly toward PG, or land
  it with a named, measured, EXPLAINED regression per R10 DESIGN §7.
  ORIGINAL R16 SCOPE: adjudicate
  `TestSlice3FilterColumnSurvivesNarrowing` and
  `TestOwnedBuildPoisonPrebuiltBoundary`, then land flip + provenance
  (K20) + stage pin in ONE commit and run R10 DESIGN §5's gates.
  ORIGINAL R15 SCOPE: the same way
  (`TestSlice3FilterColumnSurvivesNarrowing`,
  `TestSplitEqualityForHashMultiKey`,
  `TestOwnedBuildPoisonPrebuiltBoundary`), then land flip + provenance
  (K20) + stage pin in ONE commit and run R10 DESIGN §5's gates.
  CAREFUL with `TestSplitEqualityForHashMultiKey` ("fell back to Nested
  Loop") — losing a hash join is a shape regression unless PG declines
  it too; it is synthetic, so PG's answer must be obtained by running
  the equivalent SQL on :65432. ORIGINAL R14 SCOPE: (not against the
  new output: the criterion is whether the flip's shape is PG's shape),
  then the 2 stage pins + regenerate `scripts/planner-flags.env`, then
  land the flip and run R10 DESIGN §5's gates. Known already: TPC-H
  values identical under the flip; R8's counts quarantined. ORIGINAL
  R12 SCOPE: start
  with `TestSeamPlansARightLinkInsideOneSearchProblem` (outer-join
  null-extension — the class where a wrong answer is silent). Then land
  the flip and run R10 DESIGN §5's gates. NOTE: TPC-H values are
  already known identical under the flip. ORIGINAL R11 SCOPE: reading
  each expected tree against PG rather than against the new output.
  Then land the flip and run R10 DESIGN §5's gates. Everything already
  known is in R10's report so it need not be re-derived.
- [ ] ~~R22 — slice (B)~~ **MERGED INTO R21** (K24). Was: (K12
  remainder, LARGEST identified lever). Convert HashAggregate to
  GroupAggregate where the order is owed anyway. Needs the upper
  planner to compare paths by PATHKEYS; today `createWindowPaths` takes
  a finished Node and `windowsetoppaths.go:19` records that above the
  search seam inputs carry no pathkeys. Architectural.
- [ ] **R22 — heap page fill on bulk load** (K14 remainder). goopg
  leaves ~21.9 bytes/row of free space PG does not (~15% on
  `store_sales`). Compare free space per page directly on both engines
  — do NOT infer from totals again. On-disk question, not planner.
- [ ] **R23 — `character(N)` blank-padding** (R5 §2.1). An on-disk
  PG-compat defect in its own right; shifts `relpages` on every
  `bpchar` table.
- [x] **R21 — index-leaf repricing hole** (§7.2, `joinsearch.go:480`) —
  **DECLINED 2026-09-09: no witness (census-measured).**
  `r21-index-leaf-qpqual/REPORT.md`. Implemented, unit-pinned, A/B'd,
  reverted: instrumented census counts 1282 leaf pricings across both
  corpora with ZERO index leaves (TPC-H 100/100 SeqScan; TPC-DS
  1107/51/17/7 Seq/CTE/Project/SetOp), and pre/post-R21 TPC-H captures
  are byte-identical 22/22. Prebuilt leaves are bare SeqScans;
  rule-based index choices carry absorbed bounds, not Filter chains
  (`rewriteScanInputsWithSingleTablePredicates` + C-02c splice-out,
  read). Follow-up filed: absorbed-leaf `index_qual_cost` needs its
  own design (Filter-chain counting cannot reach it by construction).
  ORIGINAL: Give index leaves their qual charge instead of
  `numQualOps = 0`.
- [x] **R22 — unconditional plain-index-scan arm** (§7.3) —
  **DECLINED 2026-09-09: provably unwinnable (dominance proof +
  production probe).** `r22-plain-index-arm/REPORT.md`. Implemented,
  unit/driver-pinned, probed (145 offers / 0 survivals across TPC-H),
  TPC-H A/B zero bytes, reverted in full. A full-fetch index scan is
  strictly dominated by seq on both cost axes whenever a seq path
  exists (always); PG's own comparator prunes the same way (live PG
  shows zero cond-less index scans on TPC-H). The Q5/Q8 nation sites
  are ordered-arm contests, not plain-arm (plus a width-model gap at
  Q8 store: 676 vs 20). ORIGINAL: Drop/relax the `hasUsefulPathkeys`
  gate so a plain index path is always a candidate.
- [x] **R23 — persist correlation** (§7.4) — **STALE 2026-09-09, no
  code change (K27).** Live-verified on a private SF0.5 clone:
  `ANALYZE store_sales` writes slot 3, stop+restart restores
  byte-identical correlation (`ss_sold_date_sk` 0.14367048). The
  write/decode/restore chain is complete at HEAD; the row's premise
  predates the slot-3 writer. What remains is operational (bench heaps
  need re-ANALYZE — NOT done: shared measurement state) and out of
  planner scope. ORIGINAL: Connection-scoped ANALYZE loses correlation
  across restart → `corr = 0` → every index scan at `max_IO_cost`
  (`costindex.go:407-420`).
- [ ] **R24 — re-measure the ONEREL flip.** E-21 Cut 1b routes
  single-table statements through the search behind `GOOPG_ONEREL_SEARCH`
  (default OFF, deliberately — removing the rule chooser made plans
  worse under the §3 asymmetry). After R1/R2 change the prices, re-run
  the flip A/B (values + parallel-mode Gather capture + timing). The
  diversion may become closable.
- [~] **R25 — decompose the NLI node** (owner direction 2026-09-09).
  Design `r25-nli-decompose/DESIGN.md` (this round's first deliverable):
  replace `NestedLoopIndexJoin` (74 referencing files, PG has no such
  node) with `Join{Algo:NestedLoop}` + parameterized IndexScan, unifying
  tuple passing on OuterColumnRef + repointed slot (the lateral
  dialect, PG's nestloop params). Slices: planner construction →
  executor driver (keeps emit-once semantics, Memoize, deform bounds,
  TID walks) → EXPLAIN Index Cond → deletion + cost/whitelist
  migration (terminatesPartial entry goes; partial-NL execution stays
  deferred — E-20 Cut 4's missing worker story, not removed by this).

## Log

- 2026-09-09 (CC) **K31 census done**: 63 CTE declarations across 30
  TPC-DS queries; **40 (63%) single-reference** = the inlinable set;
  23 multi-reference (PG materialises those too). Also corrected R28 §2:
  Q77 is 4-of-6 single-reference, not 6-of-6 — I had inferred that from
  PG's plan without checking the query. A first census said 22 because
  the regex counted each CTE's own declaration as a reference.
- 2026-09-09 (CC) **R28 finding — goopg MATERIALISES CTEs where PG
  INLINES them** (`r28-cte-inlining/FINDINGS.md`). Diagnosing
  `outer-over-derived` (a deliberate firewall, NOT a bug — resume
  condition already written as "B-06 CTE-output stats") found the
  structural cause underneath: **PG's Q77 has NO `CTE Scan` nodes** —
  PG 12+ inlines single-reference non-recursive CTEs. Corpus-wide
  TPC-DS: **goopg 111 `CTE Scan` vs PG 68**. goopg's
  `pushQualsThroughSingleRefCTEs` reproduces `inline_cte`'s QUAL-PUSHDOWN
  half and leaves the node standing; PG's replaces the reference. So
  goopg matches the row counts, not the shape. **K31.**
- 2026-09-09 (CC) **Decline re-audit** (`r26-seam-decline-audit/FINDINGS-3-reaudit.md`):
  **13 declines / 7 queries -> 9 / 5.** Q49 and Q93 are now ADMITTED —
  they moved from ineligible to eligible, the axis category counts
  cannot see. Survivors: `outer-over-derived` 3 (largest, take first),
  `outer-spine` 2, `outer-link-no-sjinfo` 1 (Q78 — DIFFERENT cause from
  Q49's), `lateral` 1 (Q68 — check for another bound-ref-read-as-
  escaping case first). TPC-H still 0.
- 2026-09-09 (CC) **R27 §4a SHIPPED — the demotion reaches the plan.**
  Q49: seam declines 3->0, `Hash Left Join` x3 -> gone (5 Nested Loop +
  1 Merge Join vs PG's 6 Nested Loop) — it is ELIGIBLE again. TPC-DS
  declines 13->9; join-method 74->73; scan-type 71->70; TPC-H values
  byte-identical; sweep PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=0. Match count unmoved, exactly as DESIGN §6 predicted.
  **K30**: LEFT->ANTI is not plan-complete — PG's conversion also drops
  the forcing `IS NULL` qual; goopg leaves it and filters every row
  (0 where 1 is correct), so only INNER verdicts are transplanted.
- 2026-09-09 (CC) **R27 SHIPPED — a row-dropping demotion fixed**
  (`r27-outer-join-reduction/REPORT.md`). RIGHT/FULL joins judged their
  NULLABLE arm against `accumulatedNN`, which carries ON-strictness
  from INNER joins BELOW; those do not survive a join that
  null-extends their result. Now judged against `upperNN` (PG's rule).
  **Oracle-verified**: PG emits `Merge LEFT Join` + 2 rows where goopg
  demoted to INNER (would return 1). Three optimizer tests pinned the
  bug and were corrected with the PG result in their failure messages.
  All 5 executor VALUES tests pass; TPC-H values byte-identical; sweep
  PASS=95 all-zero; parity unchanged (neither corpus contains the
  shape — which is why it survived).
- 2026-09-09 (CC) **R27/2 — the demotion VERDICT is wrong, and the seam
  decline is MASKING it** (`r27-outer-join-reduction/FINDINGS-demotion-is-wrong.md`).
  §4a's transplant works structurally (optimizer suite green, flip
  contract intact) but the 5 executor VALUES tests still fail.
  Instrumented: `join[1] Right -> Inner` for
  `rj_a JOIN rj_b ON … RIGHT JOIN rj_c ON … WHERE rj_a.id IS NULL` —
  destroying the only rows the query returns. Cause: `accumulatedNN`
  accumulates ON-clause strictness from INNER joins BELOW and the RIGHT
  arm reads its own nullable side as non-nullable; PG only lets quals
  from ABOVE constrain a join. **K29**: the fail-closed
  `outerLinksHaveSJInfos` decline is LOAD-BEARING — it masks an
  incorrect demotion, so removing it without fixing `applyDemotion`
  SHIPS WRONG ROWS. Work order inverted: fix the strictness
  propagation FIRST.
- 2026-09-09 (CC) **R27 design REVISED (§4a) by implementation.** The
  "analysis on a copy, write back verdicts" plan replaces
  `reduceOuterJoins` and fails 8 tests — five of which PIN the S9.4
  flip as observable (`...RightToLeftFlipFirstPosition`,
  `...RightFlipThenAnti`, ...). So the `Base<->Right` swap reaching
  `s.FromExprs` is CONTRACTUAL for the deconstruction, not an internal
  detail. Revised: SUPPLEMENT rather than replace — collect verdicts
  early from a COPY, apply join-TYPE only to the node tree, leave the
  late `reduceOuterJoins` call exactly as-is. Price: threading verdicts
  into `planFromItem`.
- 2026-09-09 (CC) **R27 design** (`r27-outer-join-reduction/DESIGN.md`)
  completes K28's audit. `applyDemotion` does TWO jobs in one mutation:
  (A) join-type demotion — safe and wanted for the plan; (B) the S9.4
  RIGHT->LEFT flip, which **swaps `Base<->Right`** and is only an
  ANALYSIS normalisation. goopg's node builder is position-sensitive
  (`SourceTableIdx`/binding offsets in FROM order), so (B) reaching it
  re-points column refs — that is the `SELECT rj_c.id, rj_a.id, rj_b.id`
  wrong-rows failure, not a moved plan. PG is immune because it
  references by `Var`. Fix: run the analysis on a COPY, write back only
  join-TYPE changes, then move the call to PG's position. Strictness
  analysis deliberately untouched — least evidence behind it.
- 2026-09-09 (CC) **R26/2** (`r26-seam-decline-audit/FINDINGS-2-outer-join-reduction.md`):
  traced `outer-link-no-sjinfo` (7 of 13 declines) to an ORDERING bug —
  `planFromClause` builds the node tree from the un-demoted `FromExpr`s
  and only THEN calls `reduceOuterJoins`, which mutates them in place,
  so the plan says `JoinTypeLeft` while `join_info_list` says there is
  no outer join. Direct parity evidence: **PG's Q49 has 6 Nested Loop
  and NO outer join; goopg has 3 Hash Left Join.**
  **Moving the call (PG's position) is NOT the fix** — it broke 6 tests
  incl. 2 on VALUES ("got 0 rows, want 1"; "a WHERE qual on a RIGHT
  JOIN's nullable arm was evaluated below the join that produces the
  NULLs"). **K28**: goopg's outer-join demotion has NEVER driven a
  plan, so its correctness as a plan rewrite is unestablished — audit
  `applyDemotion` against PG's `reduce_outer_joins` BEFORE moving it.
- 2026-09-09 (CC) **Seam-decline audit** (R26): TPC-H **0 declines**
  (so its 2/22 is purely costing); TPC-DS **13 across 7 queries**
  (Q49/Q51/Q68/Q77/Q78/Q93/Q97), top reason `outer-link-no-sjinfo` (7).
  That guard is fail-closed and correct — without SpecialJoinInfo the
  search could emit INNER where the statement wrote OUTER. Next: find
  why `joinInfoList` is unpopulated for those shapes
  (`deconstructJointreeScopedSJI`), starting with Q49.
- 2026-09-09 (CC) **Q30/Q81 CLOSED** (`r25-nli-decompose/REPORT-slice1-ctescan-fix.md`).
  Cause was NOT costing (my §4a guess) and NOT the CTE cache (two
  falsified hypotheses): slice 1's `OuterColumnRef` probe keys landed
  in a CTE BODY, so `planHasEscapingOuterRef` walking the CTEScan leaf
  reported an escaping ref, `chainCarriesLateral` fired, and **the seam
  declined Q30's whole outer 3-way join** -> legacy path -> cross
  product. Found with the existing `traceSeamDecline` channel in ONE
  run. Fix: the walk stops at a `*CTEScan` (a plain WITH body cannot
  reference the enclosing query). Q30 >300s -> 3.8s (PG 4.1s), Q81
  ->5.1s (PG 14.7s); **sweep back to PASS=95 all-zero TIMEOUT=0**.
  **K27**: a seam DECLINE is a parity signal in its own right — a
  declined query cannot converge on PG's plan by any costing work.
- 2026-09-09 (CC) **Corrected my own slice-1 conclusion.** Q30/Q81 are
  a PLAN divergence (cross product vs PG's index-scan inners), not a
  goal-sanctioned slowdown. Falsified two repair hypotheses by
  measurement (lateral CTE seeding; CTE re-materialisation — exactly 1).
  Sweep on committed slice 1: PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=2 SKIP=4 — execution correctness confirmed corpus-wide.
- 2026-09-09 (CC) **R25 slice 1 LANDED** (`r25-nli-decompose/REPORT-slice1.md`).
  Answered the handover's open question with a controlled A/B: the
  Q30/Q81 TIMEOUTs **ARE** slice 1's (base 3s/5s -> both >300s), and
  slice 1 is **not** plan-neutral as the handover recorded. Cause: base
  DECORRELATED Q30's correlated subquery into a hash join; slice 1
  leaves it a `SubPlan` per outer row. **PG emits the SubPlan too** —
  so slice 1 moved Q30 TOWARD PG and the timeout is exactly the case
  the goal rule sanctions. **Slice 2 must NOT "fix" it**; restoring the
  decorrelation would move Q30 away from PG.
  Gates: suites green; TPC-H values 22/22 byte-identical (Q12=2/Q13=34
  as base); TPC-H parity unchanged (2/20); TPC-DS join-method 75->73,
  scan-type 72->71, qual-placement 11->13, match 0->0.
- 2026-09-09 K26 §9: obstacle CLEARED — it was a test helper pinning a
  node kind its own file calls an optimisation (`nliIn` vs
  `nliProbeKeys`); the named test now PASSES with implied equalities.
  But **join-order does NOT fall** (18 -> 18), falsifying §4: the
  clauses are necessary, not sufficient. Remaining join-order work is
  COSTING. Test fix kept; seam reverted pending Slice3 adjudication.
- 2026-09-09 K26 §8: open question ANSWERED by probe. Implied
  equalities turn the pinned semi join into an NLI (legal, plausibly
  better); the test's NLI branch nil-derefs at `nliIn(nli.Inner).Key`
  because `nliIn` does not recognise the new inner shape. Obstacle
  reframed three times by measurement: "breaks layouts" -> "breaks a
  remap" -> "one helper misses one shape".
- 2026-09-09 K26 §7: enabled the transitive half and measured — breaks
  only 3 tests, and the named obstacle fails by nil-deref in its OWN
  helper (K19's 4th walker family), not by an assertion. Descending
  joins does not find the semi join, so it is not merely relocated.
  Seam reverted to constants-only, suites green; walker fix kept.
- 2026-09-09 K26 measured: join-order's dominant cause is that goopg
  never synthesises implied join equalities from its equivalence
  classes, so star-shaped queries can only be joined through the fact
  table. Next round: port `generate_join_implied_equalities`.
- 2026-09-09 Roadmap note added (`ROADMAP-to-all-match.md`): measured
  that reaching ALL-match is a conjunction of 6 category programs plus
  2 storage items. join-order blocks 95/99 TPC-DS and 17/22 TPC-H and
  is untouched. Explains why R1/R3/R6/slice-2b were each correct and
  each moved the match count by zero — that is arithmetic, not failure.
  TPC-DS under the flip+splice: no crashes, parity unchanged.
  Values gate green for the committed default state (PASS=95 all-zero).
- 2026-09-09 **Slice 2b DONE (attempt 4, splice) — K23 CLOSED.** The
  flip no longer costs the Partial/Finalize split: aggregation-strategy
  14 -> 10, 22/22 plans build, Q9's aggregate matches PG exactly.
  Values 22/22 identical under the flip. That was R10 DESIGN §7's last
  documented objection to landing the flip.
- 2026-09-09 Slice 2b attempt 3: diagnosis COMPLETE. The derivation was
  correct; my helper discarded the Project computing the group key.
  Narrow (immediate-Gather) form is sound but inert. Correct fix
  identified: splice the Gather out of the chain rather than descend to
  its child. Landed narrow+enabled (safe), gated byte-identical.
- 2026-09-09 Slice 2b attempt 2 also failed, measured: clearing the
  agg spec's stamp changes nothing because the target is stamped
  POST-HOC on the emitted node. Question narrowed to why
  `deriveAggregateInputKeep` returns empty-but-KNOWN. Next attempt
  starts in group_input_target.go, not the producer.
- 2026-09-09 Slice 2b written and measured, landed DISABLED. The unwrap
  design supersedes DESIGN §9 (no partial Path needed — the partial
  plan is the Gather's child). K23's success test MET
  (aggregation-strategy 14 -> 10) but 2 queries crash on a stale
  aggregate input target; fix named, assertion kept.
- 2026-09-09 Slice 2b fully specified (DESIGN §9) but NOT implemented:
  site, seed, existing shape to reuse, and the double-divisor trap all
  recorded. Stopped short of the construction deliberately — a
  mis-split parallel aggregate returns wrong rows rather than an error.
- 2026-09-09 Slice 2b prerequisite landed: `searchedRelOf` accessor.
  The naive implementation returns nil (the child is a Project wrapping
  the search root) — caught by probing, which would otherwise have made
  slice 2b look like a costing problem. Measured under the flip:
  partialPaths=1, so K23's input is confirmed present.
- 2026-09-09 R21 slice 2a LANDED, and cheaper than designed: the rel
  rides the `searchedTree` tag (the pattern the codebase already uses
  for `searchPathkeys`), so planner.go was never touched. Gate
  byte-identical both corpora. Yielded K25 — a rel on a node is a
  gateway to the path graph for every generic walker.
- 2026-09-08 Slice 2 scoped, not started: the rel's route to the
  consumer crosses `planner.go` (`createGroupingPaths` takes no rel
  today). Hop table recorded in the design §8 and split into 2a
  (plumbing, byte-identical gate) and 2b (behaviour), for the same
  reason slice 1 was split — the invasive half gets an absolute gate.
- 2026-09-08 R21 slice 1 LANDED: the search's RelOptInfo now leaves
  `planJoinlistSearch` instead of dying there. Purely additive — gate is
  byte-identical plans on both corpora, and it passes. First shipped
  code toward K24. (The initial TPC-DS diff was K18's temp-path
  artefact against a pre-R9 baseline, not plan movement.)
- 2026-09-08 R20 done: K23's cause is one guard —
  `subtreeHasGather(child)` in `addPartialAggSplitPath` — and it is
  deliberate, not an oversight; relaxing it gives a double-Gather, not
  PG's plan. Crucially, K23 and K12(B) turn out to be the SAME root
  cause (K24): the upper planner gets a finished Node instead of the
  join rel's paths. Two rounds merged into one seam item (R21).
- 2026-09-08 R19 done: the last unexplained objection to the flip is
  discharged. aggregation-strategy 10 -> 14 is four queries (Q5/Q9/Q12/
  Q19), each also LOSING a category, and the cause is that the flip's
  Gather placement bypasses the partial-aggregation producer — goopg
  loses a Partial/Finalize split it already had and PG emits (K23).
  Wiring gap, not costing. Fix R20 first so the flip is strictly toward PG.
- 2026-09-08 R17 done: CORRECTS R16/K21. On real data the flip gives
  goopg PG's multi-key structure (Parallel Hash Join, both hash keys,
  same Join Filter, same worker count) — the nested loop is only at the
  test fixture's synthetic counts. Ninth falsified claim, new variant
  (K22): the caveat was written and then reasoned past.
- 2026-09-08 R16 done (findings only): PG hash-joins the multi-key
  shape, so goopg's nested-loop fallback under the flip is a CONFIRMED
  divergence — reversing R15's provisional disposition. The flip now
  has one measured gain (R14) and one measured loss (K21), and cannot
  land on the mechanism argument alone.
- 2026-09-08 R15 done (findings only): the multi-key "fell back to
  Nested Loop" is the COST arm, not the capability arm — the test's own
  header explains the one-row-floor mechanism. Narrowed from "possible
  shape regression" to "cost adjudication pending"; PG measurement for
  the synthetic shape still owed. 2 of R10's 7 now adjudicated, 1
  narrowed.
- 2026-09-08 R14 done (findings only): Q9 adjudicated against PG.
  PG uses Parallel Hash Join twice; goopg with the flip uses it three
  times; without it, none. First evidence (not just principle) that the
  flip is the PG-faithful direction. Join order unaffected and still
  wrong, so no MATCH — as predicted in advance.
- 2026-09-08 R13 done (findings only, nothing committed as code): the
  provenance artefact must land WITH the flip or the stamp lies; and
  mode=off still produces a Gather because the post-pass is ungated
  (K20), so that test's control arm cannot get a serial baseline at any
  setting. R10's set stays at 7, now fully characterised.
- 2026-09-08 R12 done: 8 -> 7. The outer-join null-extension failure —
  R11's "most likely genuine defect" — was K19's fourth walker.
  4 of the remaining 7 show REAL plan movement and need adjudicating
  against PG; the other 3 are mechanical. Flip still reverted.
- 2026-09-08 R11 done: R10's 15 failures triaged 15 -> 8. Seven were
  one bug in three Gather-blind walkers, one of them production
  (`boundaryWalkChildren`). Fixes landed, green at the shipping
  default. Flip still reverted pending the 5 substantive claims among
  the remaining 8.
- 2026-09-08 R10 attempted and REVERTED: flipping GOOPG_GATHER_PATHS to
  `all` fails 15 tests pinning the pre-flip world, in >=3 classes,
  including the test written FOR this mechanism. Reverted rather than
  bulk-updating expected outputs; tree green, nothing half-done, list
  captured for R11.
- 2026-09-08 R9 done: partial-producer jointype filter landed, pinned
  by cross-predicate test rather than a second hand-written list.
  R8's crash reproduction plans; default-off byte-identical both
  corpora; values green both. R10 (the flip) unblocked. Also killed a
  measurement artefact (K18) that made identical captures diff.
- 2026-09-08 R8 done (findings only): the partial-path CONSUMER is off
  by default, parked on a timing decision this goal's rule voids.
  Probed `=all`: Parallel Hash Join 0 -> 132 on TPC-DS (PG 139).
  Flipping it is BLOCKED by K17 — a real crash on TPC-DS Q5, because
  addPartialHashJoinPath never filters its jointype and files RIGHT
  hash joins as parallel-aware. Found by probing before flipping.
- 2026-09-08 R7 done: parallel-awareness plumbed onto the plan node and
  PG's generic prefix rendered. Premise FALSIFIED as the design said it
  would be if the label did not appear — goopg emits Parallel Hash Join
  0 times on either corpus. K15 corrected to K16: goopg parallelises by
  STAMPING a Gather over a serial subtree, PG by building partial paths
  and letting them win. No parity movement; values green both. Fifth
  falsified hypothesis, and the second in a row killed by the check it
  asked for rather than surviving as an assertion.
- 2026-09-08 K15 recorded (investigation, no code): with the corpus
  fully parsed, `parallelism` is TPC-H's top divergence category (18)
  and Q14 differs on it ALONE. goopg has a cooperative parallel hash
  build already; what is missing is that `optimizer.Join` carries no
  parallel-awareness field, so the flag dies at plan construction and
  EXPLAIN cannot print `Parallel Hash Join`. Queued as R7 — cheapest
  round with a concrete target, and self-verifying.
- 2026-09-08 R6 done: window Sort moved from executor into the plan
  (PG's create_one_window_path shape) behind a fail-closed Presorted
  flag. 13 Sorts appear below WindowAgg on TPC-DS (PG: 6);
  GroupAggregate unchanged at 13, parity unchanged, values green both.
  All four advance predictions held. An existing pointer-walk test
  caught an over-eager first cut that sorted before EVERY window —
  PG only sorts when the ordering is not already satisfied. The 13-vs-6
  residual now quantifies slice (B), which becomes R7.
- 2026-09-08 R5 done (findings only): 9 probe tables, both engines.
  `character(N)` padding CONFIRMED divergent; `numeric` FALSIFIED
  (matches exactly across column counts, digit counts, NULLs). The
  fact-table 15% has no representation explanation and is reassigned to
  heap page FILL. Fourth falsified hypothesis of mine — and the first
  that was labelled a hypothesis in advance and killed by the check it
  asked for, i.e. the process working.
- 2026-09-08 R4 done (findings only): K11b falsified — goopg DOES
  implement compute_parallel_worker. Real cause is HEAP DENSITY (K14):
  identical reltuples, relpages 29,761 vs 25,928 on store_sales, which
  straddles PG's 3/4-worker band boundary. Systematic by column type;
  all-integer table matches to 0.2%. `relpages` is a planner input, so
  **part of this goal is not planner work** — surfaced early on
  purpose. Third falsified claim of mine, all the same shape.
- 2026-09-08 R3 done: PG's hashagg spill arm landed; TPC-DS
  GroupAggregate 1 -> 13 (PG 100), TPC-H unmoved, parity flat both
  corpora, values green both. Prediction only partly held (called the
  rise substantial; it closes ~12%% of the gap). The measurement then
  exposed K12 — PG's sorted aggregate wins on ORDERING, not spill —
  which becomes R4; later rounds renumbered. K13 filed.
- 2026-09-08 R2 done: instrument fixed (UNPARSED 0/0) AND the parity
  target corrected twice (K9 stale serial TPC-H fixture; K10 128x
  work_mem gap on TPC-DS). All R0/R1 parity numbers superseded; the
  honest baseline is TPC-H 2/20/0/0 and TPC-DS 0/72/0/24/3. Advance
  prediction held. Hand-adjudicated 5 queries per corpus, yielding K11's
  four systematic causes; R3 (sorted aggregation) and R4 (worker count)
  inserted ahead of the remaining costing rounds, later rounds renumbered.
- 2026-09-08 R1 done: implemented + gated + measured. Values green on
  both corpora; parity unmoved on both. Caught and discarded a
  contaminated capture pair (measured the R0 binary — K5) and a
  sign/population defect in the parameterised site. New knowledge
  K5-K8; R2 inserted (comparator vocabulary) and later rounds renumbered.
- 2026-09-08 R0 done: baseline captured (TPC-H 1/12/8 over 21; TPC-DS
  0/96 real matches; 36/70/86 unplannable on both). Subagent delegation
  unavailable in this environment (Task call cancelled) — R0 executed
  directly, all foreground. Servers :5543/:5544 left running.
- 2026-09-08 R0 started: K1/K2 verified against tree + oracle; tooling
  confirmed. Branch `plan-parity-with-pg-take2`.

## R29 — binder-aware escaping-outer-reference guard (done)

`r29-escape-guard-binders/`. Eligibility axis. `lateral` seam declines
1 -> 0 (TPC-DS total 9 -> 8); TPC-H values byte-identical; SF0.5 sweep
all-zero; parity scan-type 70 -> 71 (Q68 only, see K33).

- **K32 (oracle-established, corrects K27).** `LATERAL` governs the
  visibility of SIBLING FROM items only. An enclosing QUERY LEVEL is
  visible from a CTE body and from a non-lateral derived table by
  ordinary correlation — verified against PG 18.3 on :65438:
  `SELECT 1 FROM store s WHERE EXISTS (WITH c AS (SELECT s.s_store_sk) SELECT 1 FROM c)`
  is ACCEPTED, while `WITH c AS (SELECT s.s_store_sk) SELECT 1 FROM store s, c`
  is REJECTED ("missing FROM-clause entry for table s").
  Consequence: K27's `*CTEScan -> return false` was unsound (correct on
  TPC-DS only because those CTEs are top-level) and is now deleted.
  **No "this node kind is a scope boundary" shortcut is sound.** Bind
  by binder, not by node kind.
- **K33 (new, costing).** TPC-DS Q68 `customer`: goopg prices a
  `Bitmap Heap Scan` below an `Index Scan using customer_pkey`; PG
  takes the index. Isolated by R29 — the decline had been hiding it.
  First clean bitmap-vs-index costing divergence in this workstream.
- **Method note.** Decline censuses are only comparable at equal
  timeouts: the "9" of R26-R28 was a 60 s reading. Re-measure BOTH
  sides of any decline A/B in one protocol.
- **Method note.** A fail-closed fallback in a partially-enumerated
  walker can make things WORSE, silently: R29's first cut enumerated
  six node kinds, missed `*Aggregate` (the shape of every TPC-DS CTE
  body), and drove `lateral` declines 1 -> 4. Prefer a generic walk
  over a hand-enumerated one whenever a sibling walker already owns
  the inventory.

## R30 — ANALYZE sample sorted into physical order (done)

`r30-analyze-physical-order/`. Statistics axis. PG's `compare_rows`
re-sort (analyze.c:1312-1322) was missing, so the correlation statistic
collapsed toward 0 for every relation bigger than the sample cap.
`customer.c_customer_sk` 0.0737 -> 1.0 (PG: 0.999908).

- **K34 (new, top blocker).** join-method is the largest movable
  category on both corpora (TPC-DS 79, TPC-H 14) and it GREW by 2 when
  a scan-cost input was corrected — so the divergence is in
  nestloop-vs-hash/merge pricing, not scan pricing. Next round.
- **K35 (method, important).** A statistics change cannot be A/B'd
  against stored stats: re-ANALYZE BOTH sides under a pinned
  `GOOPG_ANALYZE_SEED`. Re-ANALYZing alone moved TPC-DS join-method
  74 -> 77 with the binary fixed. A/A floor: category counts identical,
  plans differ only in estimate digits on 5 queries.
- **K36 (baseline changed).** Both parity clones were re-ANALYZEd in
  R30. Under the fresh, seed-pinned protocol TPC-H reads match=1/22;
  earlier rounds' 2/22 was against stale stored stats and is not
  reproducible. Quote the fresh protocol from here on.
- Statistics axis, still open: ANALYZE never visits indexes, so
  `estimateIndexGeometry` synthesises relpages/reltuples/tree_height.

## R31 — parallelism census + heap-density root cause (investigation)

`r31-parallelism-and-heap-density/FINDINGS.md`. No code change; ends on
an owner decision.

- **K37 (largest structural axis).** 66 of 99 TPC-DS queries: PG plans
  parallel, goopg serial; ZERO the other way. `Parallel Hash` goopg 0 vs
  PG 314. This one divergence feeds join-method, scan-type,
  aggregation-strategy and sort-strategy simultaneously — the four
  biggest categories after join-order are largely ONE cause.
- **K38.** The machinery exists behind `GOOPG_GATHER_PATHS` (default
  off). At `all`: Gather 42->104, Parallel Hash 0->167. But parity does
  not improve (parallelism stays 90, join-method 79->82). Flipping the
  flag is NOT the win by itself.
- **K39 (measurement integrity — affects every TPC-DS cost result to
  date).** goopg plans 4 workers 49 times; PG never plans 4. Both
  `compute_parallel_worker` transcriptions are faithful; the INPUT
  diverges. goopg's on-disk `store_sales` is 29761 pages (48 tuples/page)
  vs PG's 25928 (55-56). Refuted by measurement: tuple encoding (numeric
  and int tables pack identically in both), fillfactor (100 both), COPY
  (56 rows/page both), NULL density. Confirmed: re-inserting the rows
  with the CURRENT binary gives 25830 pages — PG-faithful to 0.4%. The
  bench data was loaded by an older binary and `VACUUM FULL` does not
  rewrite the heap, so it is frozen in. `relpages` also drives
  `cost_seqscan`, so every fact scan is priced ~15% high while
  dimensions (`item` 736 vs 1284) are priced low — a non-cancelling
  distortion under every TPC-DS cost A/B so far, R30 included.
  **RESOLVED, and the priority was WRONG — see R31b below.**
- **K40 (minor, found in passing).** `round(double precision, int)`
  resolves on goopg but does not exist in PG 18.3.
- **Open, unestablished.** goopg's `VACUUM FULL` accepted the command
  and repacked nothing; defect vs documented no-op not determined.

## R31b — private clone rebuilt; heap confound removed (done)

`r31-parallelism-and-heap-density/REPORT-fresh-clone.md`. R31 called the
reload an owner decision and treated it as blocking; that applied only
to the SHARED cluster. The PRIVATE parity clone was rebuilt with the
current binary (same TSVs, same schema — 25 tables / 24 indexes verified
on all three clusters).

- **K39 CLOSED.** Fact-table pages now PG-faithful to 0.4%
  (`store_sales` 29761 -> 25866 vs PG 25928). 4-worker plans 19 -> 0,
  matching PG, which plans none.
- **K39a — the correction that matters.** Fixing it did NOT move parity:
  every category identical except qual-placement 13 -> 12, `match=0`
  unchanged. A 15% error in every fact table's `relpages` — a direct
  `cost_seqscan` input — changed almost nothing about plan choice. The
  remaining divergence is in the COST COMPUTATION and PLANNING LOGIC,
  not in the statistics fed to them. R31's "re-baseline before
  continuing" recommendation is withdrawn.
- **K41 (new).** Dimension tables diverge the OTHER way and are still
  unexplained: `customer` 1979 pages vs PG 2872, `item` 716 vs 1284.
  goopg packs wide-varchar rows more tightly than PG. Was masked while
  the fact tables erred in the opposite direction.
- **Baseline.** Use the fresh clone for parity from here. R30's TPC-DS
  numbers were measured on the inflated cluster and should be
  re-measured before being built upon.

## R32 — K37 campaign slice 1: targetlist subplan display (IMPLEMENTED, all gates pass)

`r32-targetlist-subplan-display/` (`DESIGN.md` reviewed
APPROVE-WITH-NOTES, notes applied; `REPORT.md`). Project-Targets
visit in both EXPLAIN text walkers (TEXT only; JSON out of scope).
Q9: 6 -> 66 lines, 0 -> 15 InitPlans (InitPlan 1..15, PG placement);
newly visible serial Aggregates make K44 measurable. Test 2
REFUTED/reclassified: Q1/Q32/Q81/Q92 sublinks are Filter/Join-Filter
decorrelation-vs-SubPlan (planner strategy, referred onward).
Gates: units pass; TPC-H spotcheck Q12/Q13 PASS; SF0.5 sweep PASS=95
MISMATCH=0 (plan-shape: 98 same, changed=Q9); TPC-DS A/B only Q9
changed; TPC-H A/B 22/22 identical.

## R33 — K37 campaign slice 2: parallel pass into uncorrelated sublinks (IMPLEMENTED, all gates pass)

`r33-subquery-parallel-pass/DESIGN.md` (reviewed 2026-09-09:
REJECT -> revised -> REJECT -> revised -> APPROVE-WITH-NOTES, notes
applied; design commit `b03657c39`). K44: Q9's 15 InitPlans serial
(PG: Finalize->Gather->Partial->Parallel SeqScan, corpus-max 15).
Layer forced by elimination: R32-binary all-mode capture
(`/tmp/pp2/k37-ds-all-r32.plans.txt`) still 0 gathers in Q9 —
one-rel shapes never enter the path search (C-19h), only the
post-pass can reach them. Change: recurse `MaybeAddGather` into
`IsNonCorrelated` sublink Plans via copy-on-write graft (expr +
plan COW, Plan-pointer seen-set, fail-closed rebuilder), only on
non-strip outcomes; safety descent pinned to `walkExprRefs`/
`scopeDescend`; `MultiAssignSubqRow` out; Result/IndexCond/
IndexOnlyScan walker arms + coverage test; correlated excluded.
`r32-targetlist-subplan-display/DESIGN.md` (reviewed 2026-09-09,
APPROVE-WITH-NOTES, notes applied). Fresh-clone census (pinned env):
PG-live 180 Gather / 162 PHJ lines; goopg default 42 / 0; `all` 104 /
162; ZERO goopg gathers where PG has none. MISS set partitions into
four classes:

- **K42 (this round, display).** Q9: PG 111 lines / 15 Init-Sub / 15
  Gathers (all under InitPlans) vs goopg 6 / 0 / 0, yet Q9 executes
  OK. Root cause is a named skip: `walkPlanFiltered:424-427`
  (+ ANALYZE twin :1549-1552) recurses through `*optimizer.Project`
  without visiting `p.Targets`; fix extends the `Result`-Targets
  precedent (:622-635) to Project in BOTH text walkers, TEXT only
  (JSON out of scope). Same-shape smaller deltas: Q1/Q32/Q81/Q92.
  Unblocks measuring the real Q9 gap (K44).
- **K43 (later slice).** Partial paths above Append: Q5 trace shows
  base partials accepted, base Gathers dominated, zero partial paths
  on join rels (no partial-Append producer). Narrow: PG uses Parallel
  Append in 6 queries, only Q5+Q76 miss.
- **K44 (later slice, unmeasurable until K42).** Partial aggregation
  inside InitPlans — Q9's real gap once displayed.
- **K45 (later slice, estimate side).** Date-range row inflation
  (Q5: goopg 8116–12121 vs PG 8–14 on same `d_date` predicate);
  attribution (estimate vs Gather-costing) before any fix, per K39a.
- **Excluded:** Q6-class misses are join-order-downstream (K26 owns).

## R33 result (2026-09-09)

`r33-subquery-parallel-pass/REPORT.md`. Graft shipped with a
priced-verdicts-only gate (found in implementation: first build
over-admitted plain-scan Gathers on 31-row/1-row subplans, Q6/Q14 —
outcome accepted only with a fresh split). Corpus effect, all
PG-conformant: TPC-DS Q9 15 splits (9 s -> 2 s) + Q44 2 splits;
TPC-H Q22 InitPlan 1 splits. Gates: units + optimizer/executor
suites (8 new tests) + TPC-H spotcheck + SF0.5 sweep (PASS=95,
MISMATCH=0) + A/B both corpora (only intended sections move).
Follow-ups named, not owned: Sort-rooted merge verdicts,
Agg-over-Gather and plain-scan sublink Gathers need the
cost-comparison round (K45 family); nested-under-parallel-top stays
serial (nesting rule).

## R34 — parse-time coercion of unknown literals (IMPLEMENTED, all gates pass)

`r34-const-fold-preprocess/` (DESIGN v2 after an agent REJECT of v1;
REPORT.md). `resolveExpr` now resolves `cast('<lit>' as DATE/TIME-family)`
to `TypedStringLit` (parse_coerce.c:232-250) instead of a runtime
`CastExpr`, which `selectivity.go:717 isConstExpr` does not admit.

- **K45 CLOSED (attribution + mechanism).** Cast bounds reached the
  DEFAULT 0.3333 selectivity, never the histogram: `d_date between
  cast(..) and cast(..)` estimated 8116 vs PG 14; TPC-H `l_shipdate <=
  cast(..)` gave exactly 6,001,215/3. Now 13. It is a PARSE-ANALYSIS
  gap, not a missing `eval_const_expressions` — PG's parser applies
  typinput to an UNKNOWN Const, so PG never has the node.
- **K46/K47 (successors, required for the corpus).** Every corpus date
  predicate uses `+ INTERVAL`, where only the lower bound folds, so the
  15 affected queries moved 8116 -> ~12121: still wrong and NUMERICALLY
  FURTHER from PG's 14. Needs `estimate_expression_value` (folds STABLE
  for estimation only; `date_in` is `provolatile='s'`, so an
  immutable-only guard folds nothing) plus a type-aware histogram
  comparison (`formatExprConstant` matches byte-equal, so a folded
  timestamp cannot match a date histogram — measured 204 vs 14).
- **K48.** Pre-existing `FoldConstants` defects, live today via
  `foldPlanConstants` (planner.go:2136) and NOT introduced by R34:
  numeric arithmetic via float64 (`1.10+2.20` -> `3.3`, PG `3.30`), no
  int4-width overflow (`2e9+2e9` folds instead of raising 22003),
  byte-wise string ordering ignoring collation.
- **K49 — WITHDRAWN as proven; see R35 §1. The evidence was void.**
  Three controlled experiments: R30 correlation (~14x), R31b relpages
  (15%), R34 cardinality (580x on 15 queries). join-order stayed at
  exactly 95/99 and 20/22 through all three; R34 moved NO category on
  either corpus. Stop spending rounds on estimate inputs expecting join
  order to follow. Target the SEARCH: enumeration order, `add_path`
  dominance, and whether both candidates are generated at all.
- **Correction to record.** My claim that `FoldConstants` was dead code
  was FALSE (grep excluded `foldconst.go`, which holds the wrapper). It
  runs at planner.go:2136 — but AFTER plan selection, so selectivity
  never sees folded quals. Only the timing differs from PG.

## R35 — metric blindness + join-cardinality divergence (investigation)

`r35-join-cardinality-and-metric-blindness/FINDINGS.md`. No code change.

- **K50 — MEASUREMENT ERROR, affects how every round is judged.**
  `pg-plan-parity-diff.py:44-62` normalises estimates OUT of the
  comparison: N1 strips `rows=`/`cost=`, N5 strips `::type`, N6 compares
  quals by (columns, operator multiset) NOT literal values. **No
  estimate change can ever move a parity verdict.** R34 is proof: 18
  TPC-DS plans changed and every category was byte-identical.
  Report `shape-delta.sh` counts ALONGSIDE category counts every round:
  shape-changed=0 means the round moved no plan at all (category zero is
  trivial); shape changes with no category movement means plans moved
  SIDEWAYS, which is a different and more interesting result.
- **K49 WITHDRAWN.** "join-order is not estimate-driven" was inferred
  from R30/R31b/R34 showing no movement. Reason CORRECTED: two of the
  three did change plan structure (6 and 10 queries), so they were not
  incapable of moving the metric — but join-order stayed at exactly
  95/99 across 16 structural changes. That is evidence join order
  resists estimate corrections, NOT proof it is estimate-independent.
  Do not cite it as proof.
- **K51 — join cardinality drops whole clause classes.** EXPLAIN on
  `orders ⋈ lineitem` shows Merge Join `rows=6001255` above an input
  scan of `rows=2000418` — three different counts in one plan.
  Constant comparisons propagate to the join estimate;
  column-vs-column (`l_shipdate < l_commitdate`) and unfolded-interval
  bounds are dropped at selectivity 1.0, though the SCAN applies
  DEFAULT_INEQ_SEL correctly (goopg and PG both estimate 479,869 for
  the equivalent TPC-DS predicate). Q12: goopg 6,001,255 vs PG 28,127.
- **K52 — the open question, deliberately unanswered.** Those are
  `EstimateRows` numbers (the plan-tree walker EXPLAIN reads), not
  `calcJoinrelSize` (the `searchCtx` method the join search consumes).
  Only the latter can affect join ORDER. Instrument it on TPC-H Q12 —
  a TWO-relation join, so zero enumeration complexity — before
  proposing any fix. Do not read the rendered number and assume the
  search saw it.

## R36 — baserel selectivity gate (LANDED, all gates pass)

`r36-baserel-selectivity-reliable-gate/` (`DESIGN.md` reviewed
APPROVE-WITH-NOTES; `STATUS.md` records where it stopped). Tree is green
at HEAD — the implementation was reverted, not shipped.

- **K52 ANSWERED: the join SEARCH shares the cardinality defect.**
  `GOOPG_JRS_TRACE` on `calcJoinrelSize` for TPC-H `orders ⋈ lineitem
  WHERE l_shipdate < l_commitdate`: `inner.Rows=6001255` (RAW), never
  the restricted 2000418. So join ORDER is chosen against a 3x-wrong
  cardinality. Source-reading suggested the opposite; only
  instrumenting the consumed value found it.
- **K53 — the naive fix is a 22x error.** Do NOT just delete
  `if !sel.reliable { return baseRows }`. `sel.value` already carries
  PG's DEFAULT_* constants; the defect is the AND COMPOSITION. The
  `…WithSource` twin multiplies conjuncts pairwise
  (selectivity.go:838-847); the plain `clauseSelectivity` twin routes
  AND through `conjunctionSelectivity`, which ports PG's punt rule
  (either bound at DEFAULT_INEQ_SEL -> DEFAULT_RANGE_INEQ_SEL,
  rangequery.go:185-192 / clausesel.c:283-286). On `x>=a AND x<b`
  without a histogram: naive = 1/3 x 1/3 = 0.111 vs PG 0.005.
  **Consume `clauseSelectivity`.** That also makes the search agree
  with the scan-level estimator, which already uses it.
- **K54 — the blocker, and a likely harness bug worth its own look.**
  Three executor tests fail under the fix
  (`TestC19fPathModelGatherExecutesAsAParallelHashJoin`,
  `TestC19fGatheredHashBuildRunsOnceAndIsShared`,
  `TestSetOpJoinPromotesToHashJoin`). They pin MECHANISMS and must not
  be re-tuned. Diagnostics were inconclusive: after inserting 4000 rows
  the plan still showed `Seq Scan on sj_ws rows=1` with NO filter — the
  inserted data never reaches `baseRows` in that harness. Find out why
  (suspect in-memory catalog `RowCount` not refreshed by INSERT) BEFORE
  judging those three tests.
- **Three tests correctly re-derived already** (work is reproducible
  from STATUS.md): two pinned the bug outright; the third
  (`…PlacementIdenticalWhenStatsAbsent`) encoded an equivalence that
  held only because of the gate, and `relsize.go:169-177`'s comment
  becomes false with it — a cold-server behaviour change, matching PG.

- **K55 — ANALYZE leaves `Stats.RowCount=0` in the executor test
  harness.** Probed directly after loading the setop fixture and
  ANALYZEing every table: `sj_ws Stats.RowCount=0 cols=4` (same for
  `sj_item`, `sj_td`) — per-column statistics ARE populated, the row
  count is NOT. With `RowCount==0`, `estimateBaseRelInfo` yields
  `baseRows==0` and everything falls to `applyRelSizeFallback`'s
  block-derived count, ~1 in this in-memory harness. That is why a
  4000-row table rendered `Seq Scan on sj_ws rows=1` with no Filter,
  and why both R36 diagnostics (add rows / add ANALYZE) were inert.
  Related known shape: `internal/initdb/open.go` builds
  `TableStats{Columns: ...}` with `RowCount` left zero on restore.
  **Consequence:** the three blocked executor tests cannot be repaired
  by data or ANALYZE; they currently measure the reliability gate, not
  their own mechanisms (at HEAD `sj_item` keeps 5 rows and `sj_td` 11
  ONLY because the gate discards the default). Fix/characterise
  RowCount first, then seed those fixtures via `SetTableStats` at sizes
  where the promoted plan is genuinely cheaper, then re-run R36.

- **K56 — ANALYZE cannot see same-transaction rows (a PG divergence).**
  Refines K55. `SELECT count(*) FROM sj_item` returns 3 while an
  immediately following `ANALYZE sj_item` in the SAME `*Context` stamps
  `RowCount=0 Pages=1 cols=4`. The sampling loop counts only tuples
  passing `transam.TupleVisible` (`operators_analyze.go:873`) and
  `SetTableStats` merely assigns (`catalog.go:13112`), so zero means the
  per-tuple visibility test rejected everything; `Pages=1` proves the
  pages were read. The four column entries come from the empty
  reservoir — which is exactly why populated `Columns` appear beside a
  zero `RowCount`. PG's ANALYZE uses the current snapshot and DOES see
  same-transaction rows. Invisible on the live clusters because those
  ANALYZE committed data in autocommit (`store_sales` reltuples reads
  1,439,608 correctly).
  **Consequence:** any test or tool that ANALYZEs inside a transaction
  and then reasons about cardinality silently reads zeros, and it looks
  healthy because the column stats are present.
  **NOT a prerequisite for R36** — R36 should seed its three fixtures
  with `SetTableStats` (as the optimizer-side tests do), which bypasses
  ANALYZE entirely. K56 deserves its own round.

## R36 LANDED (second attempt, after K56 unblocked it)

`r36-baserel-selectivity-reliable-gate/REPORT.md`. Baserel sizing
multiplies unconditionally via `clauseSelectivity` (NOT the
`…WithSource` twin — K53's 22x trap).

- Join estimate consistency fixed: TPC-H `orders ⋈ lineitem` Merge Join
  6,001,255 -> 2,000,418, matching its input.
- **24 plan SHAPES changed** on TPC-DS — the largest structural
  movement of any round (R34: 0, R30: 6). Net category **−2** on
  TPC-DS (parameterisation 44→41, parallelism 90→88,
  aggregation-strategy 83→82; join-method +1, scan-type +1,
  sort-strategy +2) and **−1** on TPC-H (join-method 14→13).
- Gates: units 44 ok; TPC-H values byte-identical; SF0.5 sweep PASS=95
  all-zero, verdict-changes=none, runtime −2.3%.
- **K49's suspicion now has real evidence.** join-order held at exactly
  95/99 and 20/22 through R30, R31b, R34 AND R36 — 40+ structural
  changes, zero movement. Target enumeration and `add_path` dominance
  next, not cost inputs.
- **K57** `inferAnchoredEqualities` rule (2) is now vacuously true for
  default-priced filters (both defaults < 1/2) — an ENUMERATION change
  that caused the M0075/M0076 Q9 hang when it over-fired. Inert (no
  non-test caller); commented at the site. Re-derive before wiring.
- **K58** `scaleByFloat` truncates where `clamp_row_est` rounds.
- **K59** goopg-only thresholds (`nliMaxOuterRowsHeuristic`,
  `memoizeMinOuterRows`) are crossed far more often now; some shape
  changes are heuristic-driven, not cost-driven.

## R37 — two seq-scan cost models (investigation, no code change)

`r37-two-cost-models/FINDINGS.md`.

- **K60 — goopg prices seq scans two different ways, differing by the
  ENTIRE page term.** `costSeqscan` (cost_funcs.go:192) is PG-faithful
  and is called only from the path search; a one-relation statement
  never enters the search (`makeRelFromJoinlist` returns at
  `len(items)==1`) and is priced by a legacy model that omits
  `seq_page_cost*relPages`. Measured: `lineitem` 196,405.55 (search,
  = 136393 + 60012.55, PG's formula exactly) vs 60,012.55 (legacy).
- **K61 — WITHDRAWN, see the R37 correction. WRONG.** R36's Q12 carries
  `orders` at the search's 43,435.00 and `lineitem` at the legacy
  60,299.79, where the search's formula would give ~271,421. The
  largest relation is the one priced without pages, so `lineitem` looks
  4.5x cheaper to scan than it is — the exact direction that makes
  goopg hash `lineitem` where PG hashes `orders`. Candidate root cause
  for join-method and the build-side half of join-order.
- **Eliminated by probe, not argument:** Q12's build side does NOT flip
  when the estimate is corrected (hand-folded bound gives 28,724 vs
  PG's 28,127) nor when parallelism is disabled. So it is neither an
  estimate nor a parallelism artefact.
- **K62 ANSWERED — the search used the CORRECT cost.** Instrumented on
  Q12's exact shape: `SEQCOST pages=136393 tuples=6001255 qualops=5 ->
  total=271421.24`. Pages and all five quals included, PG's formula
  exactly. EXPLAIN alone renders 60,299.79. K61's "both models in one
  plan" claim described the RENDERING, not the decision, and is
  withdrawn.
- **Q12 build-side RESOLVED — see K64.** Four candidates eliminated: estimate (folded bound gives
  28,724 vs PG 28,127, side does not flip), parallelism (disabling does
  not flip it), page term (search had it). Next probe is the comparison
  itself — instrument `add_path` for both hash-join orientations and
  record both candidates' costs, or whether the PG-shaped one was
  generated at all (`planner_verify_both_candidates_generated`).
- **K63 — EXPLAIN reports a scan cost the planner did not use.**
  60,299.79 rendered vs 271,421.24 consumed: a 4.5x understatement on
  the corpus's largest relation. Does NOT affect plan choice, but it
  corrupts every cost-based artefact read here — `plan-gate
  MODE=semantic-cost`, estimate-audit tables, and any human reading an
  EXPLAIN. The cost twin of the `EstimateRows`/`calcJoinrelSize` row
  split. Reporting-integrity fix; do NOT expect categories to move.

- **K64 — WITHDRAWN, WRONG (reverse-engineered from totals). See K65.** `GOOPG_HJ_TRACE` on `addHashJoinPath` for Q12, both
  orientations as the search costed them:
  `probe=orders(1.5M,43435) build=lineitem(28724,271421) -> 328627.53`
  vs `probe=lineitem(28724,271421) build=orders(1.5M,43435) ->
  534007.10`. PG's orientation WAS generated and `add_path` correctly
  took the cheaper of the two numbers it was given — the numbers are
  what is wrong. Scan inputs are identical either way (314,856), so the
  whole difference is hash overhead: 13,771 building 28,724 rows vs
  **219,151 building 1,500,000** = ~0.146 per build row, against PG's
  ~`cpu_operator_cost` (0.0025). That term is why goopg always builds
  the small side while PG hashes 1.5M `orders` to stream the expensive
  `lineitem` scan once.
  Fix: diff `hashJoinCost` (cost_funcs.go) term by term against
  `initial_cost_hashjoin`/`final_cost_hashjoin` (costsize.c:4200-4450).
  Squarely a cost-computation fix the parity metric CAN see; expect
  `join-method` movement and possibly the build-side half of
  join-order. Do NOT expect `match`.
- **Chain of elimination, for the record** (each by measurement, not
  argument): estimate -> parallelism -> page cost -> candidate
  generation -> arithmetic. Both orientations ARE enumerated
  (`makeJoinRel` calls `addPaths` twice, joinsearchlevel.go:658/661,
  matching joinrels.c:916/919), so goopg's join search is structurally
  PG-shaped here; only the pricing diverges.

- **K65 — goopg has NO COLUMN PRUNING; rows are 20-32x too wide.
  Replaces K64 and is the largest single divergence found this
  session.** TPC-H Q12: goopg `orders` width=448 / `lineitem` width=550
  against PG's 22 / 17, for a query reading one column from `orders`
  and four from `lineitem`. At `work_mem=64MB` a 1.5M-row `orders`
  build is 641 MB in goopg (multi-batch, ~200,000 of spill I/O) versus
  31 MB in PG (single batch, no spill). THAT is the whole 205,380 gap
  that made goopg refuse PG's build side — `hashJoinCost` is handed a
  side 20-32x too wide and its spill arithmetic then behaves correctly.
  Instrumented terms: bucket 71.81 / 9,375, build ~18,750 — none of
  them the difference.
  Existing note `goopg_optimizer_no_attr_needed_no_ios_path` records
  the shape: inside a join tree there is no `Project` above the scan,
  so there is nowhere to hang a narrowed target list.
  **Blast radius:** width feeds hash geometry, every spill/batch
  decision, Gather transfer costs, sort footprints and memory budgets —
  so it perturbs join-method, parallelism AND sort-strategy at once,
  three of the four largest remaining categories. Architectural change
  is explicitly permitted for this goal.
- **Method note (cost time twice this round).** K61 and K64 were both
  inferred from rendered numbers / totals rather than instrumented
  terms, and both were wrong. **Instrument the term, never infer it
  from the sum.**

## R38 — reltarget width (REJECTED on review, then refuted by measurement; NOT implemented)

`r38-reltarget-width/DESIGN.md` + `STATUS.md`. Tree green; no code change.

- **K66 — the design targeted the wrong field.** `pathNCols` /
  `pathAvgVarBytes` (`path.go:634-658`) read `NCols`/`AvgVarBytes`,
  never `Width`. The spill term is a function of COLUMN COUNT:
  `EntryBytes = 48*ncols + 24 + avgVarBytes`
  (`hashsize/hashsize.go:144-152`). 641 MB = `1.5M x (48x9 + 24)` from
  `orders`' 9 COLUMNS, not from `width=448`. EXPLAIN's `width=` is
  `TupleWidth(n.Output())` — a symptom, not the input. Correct target:
  `RelOptInfo.NCols` + `AvgVarBytes`.
- **K67 — and even then it would NOT fix Q12.** Narrowed to the single
  needed column, goopg is 72 B/row -> 103 MB for 1.5M `orders` rows,
  which STILL spills at `work_mem=64MB`; PG is 22 B/row -> 31 MB and
  fits. Column pruning is a real 6.3x win (456 -> 72) and remains 3.3x
  above PG. The residue is `DatumBytes=48` per column vs PG's ~22-byte
  whole MinimalTuple — the existing
  `docs/design/not_ralph/minimize_datum/` workstream. **K65 is
  NECESSARY BUT NOT SUFFICIENT; it is blocked on that.**
- Review findings kept: executor sizes from the RUNTIME schema
  (`operators_join_agg.go:607-609`), so narrowing planner width cannot
  under-size the real table (hazard refuted); nothing derives a schema
  from `RelOptInfo.Width`, but `considerparallel.go:567` feeds it to
  `estScanPages` as a PAGE count, which PG never does; the node-free
  keep-set is `neededKeepSet` (`narrowoutput.go:789-806`), since
  `joinKeepSet`/`buildKeepSet` need a built node; correct placement is
  between `relfromjoinlist.go:699` and `:707` with the base path
  re-costed; do NOT mutate `RelOptInfo.AvgVarBytes`/`ColVarBytes` —
  `entrywidth.go:53-56` uses them as the executor's over-charge
  fail-safe. PG order confirmed: `set_rel_size` (allpaths.c:322)
  completes before `set_rel_pathlist` (:351).
- **Why not implemented:** the design's own prediction ("the spill term
  disappears") is now known false. Landing it alone would churn shapes
  on both corpora while leaving the target query unmoved.

## R39 — the 8 seam declines, triaged (investigation, no code change)

`r39-seam-decline-triage/FINDINGS.md`. Census unchanged by R36. A
declined query falls to the legacy planner and CANNOT match PG by any
costing work, so these 5 queries are hard-blocked from `match`.

- **K68 — `outer-over-derived` (3) is a DELIBERATE guard, blocked on
  B-06.** `relfromjoinlist.go:665`. An outer join over a derived (CTE)
  input has no statistics, so every path prices at rows=1; Q78 once
  costed Nested Loop 3.07 vs Hash 3.09 on that lie and ran 15 s -> 327 s
  TIMEOUT. Its resume note says "lift when B-06 wires CTE-output
  stats". **Verified B-06 has NOT landed**: `cte_stats_synthesis.go` is
  step 2 part 1 and its header says "nothing here is called from
  production yet ... inert by construction"; no non-test caller exists.
  DO NOT lift this decline first — it re-opens a measured 20x timeout.
- **K69 — `outer-link-no-sjinfo` (3)**, `joinsearchseam.go:477`: outer
  links with no matching SpecialJoinInfo (the fail-closed
  `outerLinksHaveSJInfos` guard). R27 fixed the Q49 instance of this
  class by making the PLAN and `join_info_list` agree on join TYPE;
  these three are a different instance needing their own diagnosis.
  Worked precedent exists.
- **K70 — `outer-spine` (2)**, `joinsearchseam.go:253`, when
  `splitOuterSpine` cannot peel the pinned spine. Already knowingly
  deferred at `planner.go:1515` too. Deepest of the three.
- Five of the eight sit on ONE problem
  (`web_returns,date_dim,web_page`); the other three are one shape
  across the three sales channels.
- **Recommended order: B-06 wiring -> outer-link-no-sjinfo -> outer-spine.**

### Cross-workstream dependencies (state plainly, do not re-investigate)

Two of this session's blockers terminate in OTHER in-flight
workstreams, and neither is a defect in this one's plan:

- **K65** (column pruning) needs `minimize_datum` for `DatumBytes`;
  pruning alone leaves 72 B/row vs PG's 22 and still spills (K67).
- **K68** (3 declines) needs B-06's consumer wiring.

## R40 — complete the LEFT->ANTI transplant (K69, DESIGN pre-review)

`r40-left-anti-transplant/DESIGN.md`. Root-causes K69's three
`outer-link-no-sjinfo` declines (all in Q78) by instrumenting
`outerLinksHaveSJInfos` directly (temporary trace, reverted before
commit) rather than inferring from code: all three fire on an identical
mismatch, `want jt=1(LEFT) ... have jt=6(ANTI)` on the same relids. Each
of Q78's three CTE bodies (`ws`/`cs`/`ss`) has
`<channel>_sales LEFT JOIN <channel>_returns ON … WHERE
<returns>.order_number IS NULL`; `reduceOuterJoins`'s real call
(feeding `ctx.joinInfoList`) demotes this to ANTI via S9.3, but
`demotedForPlan` — by K30's deliberate, correct-at-the-time decision —
transplants only the INNER half of the verdicts to the plan tree,
leaving the plan at LEFT. The two consumers disagree on Jointype and
`outerLinksHaveSJInfos` fails closed.

**Verified against the PG 18.3 oracle (`:65438`)**: this exact shape
plans as `Merge Anti Join` with the `IS NULL` qual dropped from the
rendered plan entirely (not a residual filter); row counts match
(323532) between the LEFT+`IS NULL` form and an explicit `NOT EXISTS`
rewrite. This is a real, currently-unrealized parity opportunity, not
an eligibility technicality.

**Confirmed why a bare Jointype transplant is unsafe** (locks down
K30's one-line reason structurally): `Join.Output()` (`plan.go:1205`)
and `joinPublishesInner` (`joinrelsize.go:136`) both drop the
inner/nullable side's columns for Semi/Anti — transplanting ANTI without
also removing `wr_order_number IS NULL` from WHERE would try to resolve
a column that no longer exists in the join's output row. Separately,
`mapJoinType` (`planner.go:6586`) has no `parser.JoinAnti` case and
silently falls to `JoinTypeInner` — both gaps must close together.

**Design proposes**: (a) `demotedForPlan` transplants ANTI too and
reports which table(s) it demoted; (b) `mapJoinType` learns
`parser.JoinAnti -> JoinTypeAnti` (not `JoinSemi` — `applyDemotion`
never produces one, so mapping it would be speculative); (c) a new
`stripForcingNullQuals`, mirroring `collectForcedNullTableNames`'s
top-level-AND-only walk exactly (so it can only drop what the decision
side already certified as forcing), removes the specific `IS NULL`
conjunct from a LOCAL copy of the WHERE clause before it is resolved
into the top Filter — `s.Where` itself is never mutated, same
discipline `canonicalizeQual` already uses one line below it.

**Adversarial review (subagent, full HEAD re-derivation) found a real
gap and it is now closed in the design.** 15 factual claims verified
against the real source, including both crux wiring questions (is
`ctx` the same object across `planFromClause` and the WHERE arm? is
`demotedForPlan`/`reduceOuterJoins` called with the same raw
`s.Where` `stripForcingNullQuals` would walk?) — both yes. But review
found (a)-(c) alone would be reachable-and-wrong for Q78 itself:
`planFromItem`'s per-item schema-carry (`planner.go:3211,3346-3347`)
does not narrow for Semi/Anti when a further join in the SAME
`FromExpr` chains onto it — exactly Q78's `<channel>_sales
LEFT JOIN(->ANTI) <channel>_returns ... JOIN date_dim` shape. Traced
to a PREVIOUSLY SHIPPED bug of the identical class
(`joinlayout.go`'s `reresolveJoinByName` doc: a stale merged
Semi-join schema corrupted Q21's NOT-EXISTS to 0 rows instead of
~411) and to an existing safe counter-pattern already in the codebase
(`unnest.go:3315` stores a left-only schema at Semi/Anti construction
rather than deferring to `Output()`). **Design now includes §4d**
(mirror that pattern + gate the loop-carry on join type), traced
end-to-end for Q78's exact shape, and §6's gate list now requires a
3-relation VALUES regression (not just 2-relation) since only the
3-relation shape exercises §4d. Full record in DESIGN.md §8.

**LANDED** (`r40-left-anti-transplant/REPORT.md`). All gates green:
sweep PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, TPC-H values
byte-identical AND plan structure identical across all 22, precommit
units green.

- **Match count did NOT move** (TPC-DS 0/99, TPC-H 1/22) and the decline
  TOTAL did not move (8). `outer-link-no-sjinfo` 3 -> 0, replaced by
  `leaf-count` 3. Say this plainly: the round bought a PG-faithful join
  TYPE and a correctness fix, not a match.
- **K71 — the round's most important outcome: goopg's S9.3 LEFT->ANTI
  rule was OVER-FIRING and would have shipped wrong rows.** PG uses TWO
  granularities deliberately (prepjointree.c:3340-3403): LEFT->INNER is
  `find_nonnullable_RELS` (relation), LEFT->ANTI is
  `find_nonnullable_VARS` + `mbms_overlap_sets` (COLUMN). goopg used
  relation granularity for both. `a LEFT JOIN b ON a.id=b.id WHERE b.y
  IS NULL` was demoted to ANTI though the ON is strict for a DIFFERENT
  column than the WHERE forces; oracle: PG emits `Merge Left Join` +
  `Filter`. The bug was already in `ctx.joinInfoList` (which
  `reduceOuterJoins` populates today) — invisible only because K30
  declined the transplant. Both shapes now match PG exactly on SF0.5
  (323,532 same-column / 325,179 different-column — genuinely different
  queries).
- **K72 — the new blocker.** The 3 declines are now `leaf-count`, all
  `nrels=2 nleaves=2`: an ANTI join's right side is not a search leaf
  (it is a pinned sub-problem) but Q78's ANTI sits at the BOTTOM of its
  chain with an INNER `date_dim` join above, so it is not a top-of-tree
  spine either and `runJoinSearchBelowPinned` does not apply. Adjacent
  to K70's `outer-spine` work; that is the next round.
- **K73 — a measurement reversed a decision that had passed review.**
  `nliCostGateAccepts`' SEMI/ANTI arm charges a B-tree descent as 1 unit
  (same as a hash probe), so it accepted Q78's 1.44M-row outer by a hair
  and ran it at 54s vs 16s. Capping the gate by outer size was tried and
  REJECTED by measurement: it also caught unnest-sourced SEMI joins,
  where the premise fails the other way — TPC-H Q4's EXISTS has a 385k
  outer but a 6M-row `lineitem` inner, and the cap made it 1.5s -> 13.1s
  (8.6x). One threshold cannot serve both populations. Landed instead:
  `Join.FromOuterReduction` marks only the joins this round creates and
  declines NLI for them; every existing decision is byte-identical and
  Q78 ends at **14s, 2s FASTER than the 16s pre-R40 baseline**. Retire
  the flag when the gate learns a real index-descent probe cost.
- Also landed: `mapJoinType` learns `parser.JoinAnti` (it silently fell
  to `JoinTypeInner`, which would have been wrong rows); per-item schema
  narrowing for Semi/Anti in `planFromItem` (review-found, DESIGN §4d —
  a chained join after the ANTI read offsets against the stale merged
  width); `stripForcingNullQuals` dropping exactly the certified
  conjuncts without mutating `s.Where`.
- **Method:** the diagnosis came from instrumenting
  `outerLinksHaveSJInfos` directly (temporary trace, reverted), per
  R37's "instrument the term, never infer it from the sum". TWO
  decisions this round were reversed by measurement after passing
  review — the adversarial review caught the schema gap, but only the
  A/B caught the NLI cap.


## R41 — the ANTI leaf-count decline is a coordinate-space mismatch (DESIGN, pre-review)

`r41-anti-leaf-coordinates/DESIGN.md`. Successor to R40's K72.

- **K74 — R40 BROKE A DOCUMENTED INVARIANT; K72 is a defect this
  workstream introduced, not a pre-existing mismatch.** Instrumented
  (trace reverted before commit), same reading for all three Q78 CTEs:
  `DPTRACE LEAFCOUNT nprefix=3 nscans=2 jl=2` alongside the standing
  `nrels=2`. Three of the four numbers are 2; only `jl.nrels()` is 3.
  R40's §4d narrowing correctly stopped emitting a `rangeBinding` for a
  SEMI/ANTI nullable side (`planner.go:3466-3471` does not advance
  `leftCtx`), but the joinlist deconstruction still allocates a LEAF
  INDEX for it — so `len(ctx.bindings)=2` while `jl.nrels()=3`.
  `fromItemRels` (`collapse.go:423`) states the violated invariant in
  its own comment: *"Exactly the number of `rangeBinding`s
  `planFromItem` appends for the same item … which is what keeps leaf
  numbering and binding order in step."* **First design draft called
  this an inherent "two coordinate spaces" condition and was refuted on
  review** — `bindings`/`scans`/`cumOffsets`/`boundaryMap`/`leafRel` are
  ALL already in collapsed space; only the joinlist and the SJI scope
  are not. Our own trace had printed `nrels=2` all along and the draft
  mis-attributed it.
- **K75 — the `leaf-count` decline is currently preventing a PANIC, not
  a wrong answer.** With `len(ctx.bindings)=2` and `nprefix=3`,
  `ctx.bindings[:nprefix]` (`joinsearchseam.go:507`, `:538`) is
  index-out-of-range. The seam panics before
  `validateJoinlistProblem`'s second net can fire. So the one-line
  "compare `len(jl)` instead of `nrels()`" fix converts a clean decline
  into a crash.
- **The fix is a RENUMBERING, not a remap** (design §4): `prob.scans[i]`
  is already the opaque ANTI `*Join` node, so a joinlist LEAF naming it
  resolves through the existing `leafRel` path with `lo,hi=i,i+1` and no
  coordinate work. Four sites: `deconstructFromItemScoped`,
  `fromItemRels`, `newSjiScope`, and dropping the ANTI's SJI from
  `join_info_list`. Plus a new `len(ctx.bindings) == jl.nrels()`
  assertion, because after the fix nothing else would catch a
  desynchronisation between the plan tree's `demotedForPlan` verdict and
  the joinlist's separate in-place `reduceOuterJoins` verdict.
- **Do NOT generalise to FULL.** A pinned FULL item keeps BOTH bindings
  (`planner.go:3470` skips only Semi/Anti), so collapsing it would make a
  leaf span two binding coordinates — that is where the real
  `boundaryMap` totality risk lives (`createplanroot.go:263` panics on an
  unfillable hole). For SEMI/ANTI there is no hole, because the nullable
  side has no binding coordinate at all.
- **The one-line fix (compare `len(jl)` instead of `nrels()`) is WRONG.**
  `nprefix` also feeds the preceding `leafRange()` equality and the
  `cumOffsets` sizing, both of which consume FROM-item indices. Changing
  its unit for the count alone would let the guard pass with the offsets
  still wrong — converting a clean decline into silently mis-resolved
  columns, this workstream's recurring failure mode (Q21's stale merged
  Semi schema; R40 §4d).
- `extractSearchLeaves` treating ANTI as opaque is FORCED, not an
  oversight: `Join.Output()` returns `Left.Output()` for Semi/Anti, so
  the right side's columns do not exist above the node.
  `joinPinned(parser.JoinAnti)` is already `true`, and
  `pinnedOverAPinnedSide`'s comment had already NAMED this exact hazard
  ("the JOINLIST side flattens while the PLAN side stops at the link and
  `extractSearchLeaves` returns one opaque leaf — the leaf count then
  disagrees with the relation count"). R40 created a new instance of a
  hazard the file predicted.
- **Even if fixed, Q78 is not predicted to MATCH**: a 2-leaf search
  cannot place `date_dim` below the anti pair the way PG's 3-leaf search
  can. Eligibility is necessary, not sufficient — same relationship R27
  §4a recorded for Q49.


### R41 implementation: BUILT, MEASURED, then REVERTED — and why (K76)

The design's 4-step fix was implemented in full (`antiCollapsedJoins` in
`collapse.go` consumed by `deconstructFromItemScoped` / `fromItemRels` /
`newSjiScope`, plus the `prefix-exceeds-bindings` fail-closed guard) and
it WORKS on its own terms:

- **`leaf-count` declines 3 -> 0; TPC-DS declines 8 -> 5**, no new class.
- optimizer + executor suites green; **sweep PASS=95 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0**, Q78 checksum unchanged; TPC-H values
  byte-identical AND plan structure identical across all 22.
- Design §6's prediction CONFIRMED against the oracle: PG places
  `date_dim` BELOW the anti join (`Nested Loop Anti Join` over
  `Parallel Hash Join`); goopg's 2-leaf search can only build
  `(sales anti returns) JOIN date_dim`. Eligibility gained, match not.

**It was reverted anyway, because admitting the body to the search LOSES
a qual placement PG has.** Q78's three `date_dim` scans went from
`rows=149  Filter: (d_year = 1998)` to `rows=73049` with no filter, and
runtime 14s -> 26s. That is a **qual-placement PARITY regression**, not
merely a timing one, so the goal's "ignore execution-time degradation"
clause does not cover it (it applies only when the plan IS PG-identical,
and this one is not).

- **K76 — root cause, instrumented (traces reverted).**
  `pushQualsThroughSingleRefCTEs` runs from `Plan()`'s TAIL
  (`planner.go:159`) on the FINAL tree, so it must descend whatever the
  body was planned into. Probes: the conjunct maps through every
  `*Project`/`*Aggregate` layer fine (`project remap ok=true`), then dies
  in the join descent — `CTEPUSH join type=0 pushable=true
  L=*optimizer.Join R=*optimizer.Project`. **The searched boundary wraps
  the scan in a `*Project`, and `pushConjunctIntoSubtreeTraced`
  (`inner_join_qual_pushdown.go:388`) has no `*Project` arm**, so the
  descent stops. `joinRestrictionSides` refusing ANTI is NOT the cause —
  no `pushable=false` was ever traced.
  This is NOT anti-specific: any CTE body the search admits hits it. It
  was simply unreachable while these bodies all declined.
- **Next round is therefore K76, not K72**: give
  `pushConjunctIntoSubtree` a `*Project` arm (mirroring the one
  `pushConjunctIntoCTEBody` already has, via
  `remapConjunctThroughProjection`). It is a shared pass, so it will
  enable pushdowns in many other searched trees at once — real blast
  radius, its own round and its own gates. **Land K76 FIRST, then R41's
  implementation on top**; in that order the eligibility gain arrives
  without the parity regression.
- The R41 implementation is fully specified by `DESIGN.md` §4 and was
  verified to build, pass every suite and pass the sweep, so redoing it
  is mechanical.


## R42 — the qual-pushdown descent cannot cross a Project (K76, DESIGN pre-review)

`r42-pushdown-project-arm/DESIGN.md`. Prerequisite for R41's
implementation.

- `pushConjunctTraced` (`inner_join_qual_pushdown.go:341`) has arms for
  `*Filter` and `*Join` only, then a terminal gated on
  `innerJoinPushEligibleInput` (`:496`) that admits `*CTEScan`,
  `*MaterializedCTEScan` or a base-relation leaf. A `*Project` is none
  of those, so the descent declines. The PG-shaped search's boundary
  republishes binding order through exactly such a `Project`, so EVERY
  body the search admits meets it — the gap was always there, R41 only
  made it reachable.
- Fix: a `*Project` arm that remaps via the SAME
  `remapConjunctThroughProjection` helper `pushConjunctIntoCTEBody`
  already uses, then recurses — mirroring the `*Join` arm's
  remap/recurse/assign shape. The helper is already fail-closed: it
  vetoes `OuterColumnRef`/`FuncCall`, requires every referenced target
  to be a plain `*ColumnRef` (so a computed or volatile projection
  declines), and name-checks both schemas.
- **Blast radius is the reason this is its own round.** The descent has
  TWO production callers: the CTE path
  (`cte_inline_pushdown.go:240,252`) and the general single-side
  inner-join pushdown (`inner_join_qual_pushdown.go:161`, `:739`) which
  runs for every statement. Movement is expected to IMPROVE
  qual-placement parity (PG pushes restrictions to baserel level), but
  that is a prediction to measure, not assume.
- **K77 — review correction that changes the round's scope: `st.proven`
  is NOT neutral.** Its ONLY consumer (`inner_join_qual_pushdown.go:167`,
  `if tr.proven && !tr.planted { continue }`) **DELETES the conjunct from
  the residual `Filter`**. Today a `Project` between a join and its input
  makes the descent return false, so the qual is unconditionally KEPT; an
  arm returning true with `proven` still true would newly REMOVE the qual
  from above on a whole class of trees — a residual-dropping MOVE, not
  the deeper placement this round is scoped to. Worse, the remap has two
  fail-OPEN seams: it skips the self-side name check for an UNNAMED ref
  (`remapConjunctThroughProjection:281`; unnamed refs demonstrably
  occur), and it never checks `len(Output()) == len(Targets)`. So R42
  sets `st.proven = false` and is deliberately COPY-ONLY. Enabling the
  move is a separate round with its own proof.
- Two more review corrections adopted: refuse `Project.IsolatedScope`
  (verbatim precedent at `upper_narrow_chain.go:373-379`; today such
  Projects are contained only by ACCIDENT — a view-rename Project
  declines because the view and body column names differ, which fails as
  soon as they match), and refuse an `Output()`/`Targets` length
  mismatch (mirroring `cte_inline_pushdown.go:207-209`), which closes
  the second fail-open seam.
- "Copies by default" means the CONJUNCT is duplicated, not the node —
  every arm already mutates in place. The real prerequisite is
  EXPRESSION FRESHNESS, which `remapConjunctThroughProjection`'s closing
  `cloneExprRefs` supplies. Passing `c` through unchanged instead would
  have aliased the caller's own expression on the CTE entry path
  (`cte_inline_pushdown.go:240/252`, where the entry node is a `*Filter`).
- **Prediction recorded before implementing:** this round does NOT
  change the decline census and is NOT expected to produce a match. Its
  success criterion is narrower — the `qual-placement` parity category
  must not worsen on either corpus and plan-shape movement must be
  explainable query by query. Match count expected to stay 0/99 and
  1/22.
- Filed, not fixed: `deriveConstAcrossJoinEquality` (`:413`, planting at
  `:739`) mutates the tree BEFORE the recursion and nothing unwinds it on
  a failed descent, so a derived sibling copy can sit below un-priced
  (the caller takes `kept = append(kept, c)` without `notePushedBelow`).
  A `*Project` arm makes deep-then-fail descents more common, so it
  WIDENS this pre-existing wart without creating it.


### R42 LANDED (`r42-pushdown-project-arm/REPORT.md`)

All gates green: precommit units, sweep **PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0**, TPC-DS verdicts+row counts byte-identical for all 99
vs HEAD, TPC-H values byte-identical AND plan structure identical across
all 22. TPC-DS total 838s -> 817s.

- **Exactly 2 plans changed (Q47, Q57), both gaining a restriction PG also
  applies at that level** — oracle-verified (`Filter: ((avg_monthly_sales
  > '0'::numeric) AND (d_year = 2000) AND …)`). So the movement is TOWARD
  PG on `qual-placement`, which was the round's stated success criterion.
  Runtimes noise-level (5454->5487ms, 2592->2609ms).
- Match count and decline census unmoved (0/99, 1/22; 8 declines) —
  exactly as predicted before implementing. This round exists to unblock
  R41, not to produce a match.
- Landed with all three review guards: `st.proven = false` (K77,
  placement-only), `IsolatedScope` refused, `Output()/Targets` length
  mismatch refused.
- **Baseline caution:** the sweep's own status-delta compared against
  R41's run, which is NOT HEAD (R41 was reverted). Against R41 it reports
  Q78 as changed; against the correct pre-R41 baseline
  (`sweep-20260909-211945`) Q78 is UNCHANGED, which is right — R42 alone
  does not admit Q78's CTE bodies. Always re-diff by hand after a revert.

- **K78 — a Go filename trap that silently disables a whole test file.**
  `pushdown_project_arm_test.go` never ran: Go reads a trailing `_arm` as
  a **GOARCH filename constraint**, so the file lands in `IgnoredGoFiles`
  and is excluded on amd64. `go test -run …` reports "no tests to run" —
  and so does `-count=1` — which reads exactly like a filter typo. Caught
  only by `go list -f '{{.IgnoredGoFiles}}'`. Any file ending in a GOARCH
  or GOOS word (`_arm`, `_386`, `_linux`, `_windows`, …) is constrained.
  Renamed to `pushdown_project_crossing_test.go`.

- **Next: re-apply R41's implementation** (specified in
  `r41-anti-leaf-coordinates/DESIGN.md` §4, already built and gate-verified
  once). With K76 closed the `date_dim` restriction can now reach through
  the searched boundary's `Project`, so the eligibility gain (`leaf-count`
  3 -> 0, declines 8 -> 5) should arrive WITHOUT the qual-placement
  regression that forced the revert. Q78 still will not match — that needs
  Option B's 3-leaf search.


### R41 LANDED on the second attempt (`r41-anti-leaf-coordinates/REPORT.md`)

Re-applied unchanged on top of R42, which removed the blocker (K76) that
forced the first attempt's revert. All gates green: sweep **PASS=95
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0**, Q78 checksum unchanged,
TPC-H values byte-identical AND plan structure identical across all 22.

- **`leaf-count` declines 3 -> 0; TPC-DS declines 8 -> 5, no new class.**
  Census read BY CLASS, not by total — R40's lesson was that a class can
  be converted rather than removed.
- **The qual-placement regression is gone**: Q78's three `date_dim` scans
  keep `Filter: (d_year = 1998)` and the query is back to 14s (it was 26s
  when R41 landed without R42). Confirms R42 was the right prerequisite
  and the revert-then-reland order was correct — landing R41 first would
  have shipped a measured parity regression to buy an eligibility gain.
- **What this bought is ELIGIBILITY ONLY.** The three admitted queries
  come out SHAPE-IDENTICAL to the legacy fallback; only costs differ,
  because the search now prices them instead of the rule-based path.
  Match count unmoved (0/99, 1/22), exactly as DESIGN §6 predicted.
- Q78 still cannot match: PG places `date_dim` BELOW the anti join
  (`Nested Loop Anti Join` over `Parallel Hash Join`) because its DP
  searches 3 leaves and can reorder into the anti pair; goopg's collapsed
  2-leaf problem can only build `(sales anti returns) JOIN date_dim`.
  That is DESIGN §6's **Option B** (teach the plan walk to descend into
  ANTI), which needs an ANTI path producer — `pinnedUnsearchable` refuses
  ANTI today — plus an "unpublished leaf" concept in the coordinate model.

**Remaining declines: 5.** `outer-over-derived` 3 (K68, blocked on the
separate B-06 CTE-stats workstream — lifting it re-opens a measured 20x
timeout) and `outer-spine` 2 (K70).


## R43 — `Parallel Hash` is the largest systematic gap left (K79, DESIGN pre-review)

`r43-parallel-hash/DESIGN.md`. Found by ranking queries by NEAREST MISS
instead of continuing the Q78 decline chain — a method change worth
keeping.

- **CORRECTION to a figure this workstream has been repeating: TPC-H is
  `match=2`, not 1/22.** Q13 AND Q6 match at HEAD. The 1/22 came from the
  R36 baseline and was carried into every later report without
  re-measurement. Authoritative at HEAD (post-R41/R42):
  `queries=22 match=2 shapediff=20`,
  `join-order=18 join-method=12 scan-type=13 parameterisation=6
  aggregation-strategy=10 sort-strategy=13 parallelism=18
  qual-placement=7 rendering=7`.
- **Q14 is ONE category from matching — `[parallelism]` and nothing
  else.** Q1 is two away (`sort-strategy`, `parallelism`). Then a jump to
  4-5 categories. Ranking by nearest-miss is how to pick rounds from
  here.
- **K79 — the gap is `Parallel Hash`, and it is the biggest systematic
  divergence found this session.** PG uses it in **69/99 TPC-DS** (310
  node occurrences) and **7/22 TPC-H** (Q3 Q9 Q10 Q14 Q16 Q18 Q21).
  goopg emits it ZERO times, structurally: `joinpathsparallel.go`'s
  header states `parallel_hash = true` is REFUSED because "no goopg
  executor builds a hash table from a partial inner", and the refusal is
  structural (the file never reads `inner.PartialPathlist`).
- **But the executor capability appears to EXIST and be live.**
  `parallel_hash_build.go` implements M0129-S4.1, a cooperative parallel
  hash build (N producer goroutines scan+filter the build table, one
  consumer owns the map), default ON (`coopJoinBuildOn`), reached from
  `joinOp` via `parallelBuildEligible`, with measured wins (Q20
  1.91->0.65s). Checked that it is not dead code.
- **RESOLVED by review — TRUTHFUL.** `coopDrivingScan` IS applied to the
  build plan (`parallel_hash_build.go:522-531`: `buildPlan := o.plan.Right`
  / `.Left`), and its "probe side" widening only governs descent THROUGH a
  nested join inside the build subtree. `:609` makes ONE shared
  `newParallelScanState`, and `:663-680` has N producers rebuild the build
  subtree with `attachParallelScan` wiring that shared atomic block
  allocator into the driving `seqScanOp` — so **each producer claims a
  disjoint block range of the build relation**, not a duplicate copy. The
  leader pre-build and the coop build COMPOSE
  (`operators_join_agg.go:666-668`), they do not compete. My earlier
  speculation that it "may never partition a BUILD scan" is REFUTED. The
  timing A/B that came back inside noise was inconclusive and should not
  have been leaned on — the source answered it.
- Two caveats: goopg parallelises the build SCAN+FILTER but insertion is
  single-consumer (PG parallelises insertion too), and
  `parallelBuildEligible` has NO parallel-mode gate, so it fires in
  SERIAL queries too where PG shows a plain `Hash`. **Therefore the label
  must follow the PATH MODEL, never observed executor behaviour.**

- **K80 — the re-scope, and rev 1's biggest error.** `Parallel Hash Join`
  needs NO `parallel_hash=true` work: `addPartialHashJoinPath` already
  sets `ParallelAware: true` (`joinpathsparallel.go:205`). It is dead
  solely because the partial-path machinery sits behind a **default-OFF**
  knob — `gatherPathModeFromEnv`'s default arm returns `gatherPathsOff`
  (`gatherpaths.go:70,82`) and every producer returns early at
  `joinpathsparallel.go:89`. **R10 already measured the flip: TPC-H
  `Parallel Hash Join` 0 -> 19, TPC-DS 0 -> 132, TPC-H `parallelism`
  18 -> 15**, with no parallel-hash work at all. The flip is NOT landed,
  and **R12 (adjudicate the 8 failures R11 left) is its hard
  prerequisite**. As rev 1 was written its step 2 would have landed code
  behind `if gatherPathsMode == gatherPathsOff { return }` — inert at the
  shipping default and unmeasurable by the sweep it named as its binding
  gate.
- **Correct order: R12 + flip FIRST**, then the residual Q14 gap (one
  node: `Seq Scan on part` -> `Parallel Seq Scan on part`) via
  `try_partial_hashjoin_path(parallel_hash=true)`, and wire
  `enable_parallel_hash` (declared at `catalog/catalog.go:12171`, default
  on, **nothing reads it** — the known declared-but-unconsumed-GUC trap).
- **Occurrence counts corrected: 9 (TPC-H) / 157 (TPC-DS) true `Parallel
  Hash` BUILD nodes**, not 18/310 — a bare `grep -c "Parallel Hash"` also
  matches `Parallel Hash Join`/`Semi Join`. Query counts (7/22, 69/99)
  stand.
- **Rendering a standalone `Parallel Hash` node buys NO parity**: the
  differ splices PG's `Hash`/`Parallel Hash` nodes out
  (`pg-plan-parity-diff.py:571-574`, N2) and excludes `Hash` from
  MISSING-NODE. Q14 needs exactly two existing flags,
  `Join.ParallelAware` + `SeqScan.Parallel`.
- **K81 — inverse-misdescription risk.** If the path model emits
  `parallel_hash=true` but `parallelBuildEligible` DECLINES at runtime
  (build < 1024 blocks, `preserveCTIDRel` set, `coopDrivingScan` nil for
  an Aggregate/Sort/index build side, or `GOOPG_COOP_JOIN_BUILD=off`),
  the plan claims parallelism the executor does not perform — a new
  misdescription in the opposite direction, i.e. exactly the "arbitrary"
  outcome the goal forbids. The planner predicate must be a PINNED TWIN
  of `parallelBuildEligible`.
- **MATCH on this metric is SHAPE-ONLY.** Q14's row estimates stay ~300x
  apart (goopg `Hash Join rows=6,001,255` vs PG `rows=18,444`; goopg's
  `lineitem` scan does not apply the `l_shipdate` selectivity at all).
  N1 pushes estimates to a side column so the differ never sees it. Any
  report claiming Q14 as a match must say this.


### R43 rev 3 — the flip MEASURED at HEAD; rev 2's sequencing claim REFUTED (K82)

R43 rev 2 relied on R10's numbers, which are stale (R40/R41/R42 landed
since). Re-measured at HEAD, TPC-H, `GOOPG_GATHER_PATHS=all` vs default:

| category | off | on |
|---|---|---|
| join-method | 12 | **11** |
| scan-type | 13 | **12** |
| parallelism | 18 | **16** |
| qual-placement | 7 | **6** |
| aggregation-strategy | 10 | **11** |
| **match** | **2** | **2** |

Net **-4 / +1**. Worth landing on the category metric, but **no new
match**, and R10's "parallelism 18 -> 15" is 18 -> 16 at HEAD.

- **K82 — Q14 is BYTE-IDENTICAL under the flip.** Still `Hash Join` over
  `Seq Scan on part`, still `SHAPE-DIFF [parallelism]`. Its `Gather`
  already comes from the partial-aggregate path, not from this knob, so
  the flip is ORTHOGONAL to Q14. **This refutes rev 2's central
  sequencing claim** that the flip was the prerequisite: the two tracks
  are INDEPENDENT, and `parallel_hash=true` (step 2) is the only thing
  that can close Q14. Either may be done first.
- **R13's scope re-measured at HEAD: 5 failing tests under the flip, not
  R12's 7** (two fixed by intervening rounds).
  `TestPartialPathIsNeverTheFinalPath` is a stage pin ("join rel 0x3 has
  partial paths before C-19d"). The four REAL ones needing adjudication
  AGAINST PG: `TestSplitEqualityForHashMultiKey/searched_enumerator`
  ("fell back to Nested Loop"), `TestSlice3LiveQ9ShapeDerivation`
  (different build sides narrowed on Q9),
  `TestSlice3FilterColumnSurvivesNarrowing`, and
  `TestOwnedBuildPoisonPrebuiltBoundary`.
- **Method note:** R10's numbers were carried forward for several rounds
  without re-measurement, exactly as the `match=1/22` figure was. Two
  stale-number corrections in one session — **re-measure before relying
  on any prior round's figures**, especially after intervening rounds
  have landed.


## R44 — `date + interval` never folds, so its selectivity is 1.0 (K83-K87, DESIGN rev 2)

`r44-const-fold-before-selectivity/DESIGN.md`. **First round aimed at
ESTIMATE parity rather than shape parity**, after five rounds closed shape
categories without moving the match count and every blocker kept
terminating in estimate-side work.

- **K83 — measured, 11.9x estimate error from one missing fold.** On the
  live TPC-H cluster:
  `l_shipdate >= DATE '1995-09-01' AND l_shipdate < DATE '1995-10-01'`
  gives `rows=78,680`; the same restriction written
  `... < DATE '1995-09-01' + INTERVAL '1 month'` gives **rows=938,645** —
  exactly the parallel-divided full table, i.e. **selectivity 1.0, the
  conjunct contributes nothing**. PG folds it in `preprocess_expression`
  -> `eval_const_expressions` and renders
  `'1995-10-01 00:00:00'::timestamp`. `date_pl_interval` is
  `provolatile='i'` — verified on the live oracle.
- **Scale: 7 occurrences across Q4 Q5 Q6 Q10 Q12 Q14 Q20; PG has ZERO.**
  Measured estimates: Q4 385,423 vs 14,974; Q12 1,500,000 vs 7,006;
  Q14 938,645 vs 18,444; **Q6 2,412 vs 28,092 (UNDER-estimates)**.
- **K84 — `tryFoldBinaryOp` DOES fold arithmetic.** My first design said
  it handled only OpAnd/OpOr; **refuted on review**. Arithmetic, concat
  and comparison all fold via `toLiteralValue` -> `evalLiteralBinary` ->
  `evalArith`; the AND/OR arms are the short-circuit cases. The real gap
  is a **type-domain** gap: `toLiteralValue` accepts Integer/String/
  Numeric/Boolean consts but NOT `*TypedStringLit` / `*IntervalLit`,
  which is what `DATE '...'` / `INTERVAL '...'` resolve to.
- **K85 — the round is TWO independently measurable steps, not one.**
  Because arithmetic folding already works, **moving the fold earlier
  changes estimates on its own**, with or without the temporal arm. Q6's
  `l_discount BETWEEN 0.05 - 0.01 AND 0.05 + 0.01` fails `isConstExpr`
  today and falls to `defaultIneqSelectivity`. So: step A = move the fold
  before the estimator; step B = add the temporal domain. Landing them
  together makes movement unattributable.
- **K86 — the fold must run on the RESOLVED `Expr` at `resolveExpr`, not
  beside `canonicalizeQual`.** The latter is **WHERE-only**: ON-clause
  join quals reach the estimator via `planJoinPredicate` -> `chainOnQual`
  -> `joinsearchseam.go` and bypass it. `resolveExpr` is the single choke
  point every qual passes (WHERE/ON/HAVING/USING), estimator entry points
  are all typed on resolved `Expr`, and it builds a FRESH tree so the
  deparsers' parse tree is untouched by construction. R34's
  `TypedStringLit` coercion in `resolveExpr`'s `CastExpr` arm is the
  shipped precedent.
- **K87 — three pieces the design assumed existed and do NOT:**
  1. **No volatility route.** `IsStrictProc`'s generated map has no
     volatility index; no `IsImmutableProc`. `BuiltinProc.Volatile` covers
     3 pg_dump fixture entries only. `initdb/pg_proc_seed_data.go` HAS
     `Volatile:` but optimizer **cannot import initdb** (initdb ->
     executor -> optimizer cycle). Must generate `pgProcVolatileByOID`
     from `pg_proc.dat` (absent ⇒ `'i'` per `BKI_DEFAULT(i)`).
  2. **No optimizer-side temporal evaluator, and the executor's is
     un-importable** (executor imports optimizer in ~91 files). Must host
     month/day/micro arithmetic in a LEAF package both can import, or
     duplicate and pin — silent duplication is the known sibling-paths
     failure mode.
  3. **The folded literal's SPELLING is an unresolved trade-off that gates
     the predicted number.** `date + interval` types as `timestamp`, but
     `numericValue`'s `"date"` arm accepts ONLY `"2006-01-02"`; a
     timestamp spelling fails to parse so `bucketFraction` returns a flat
     **0.5** — the estimate lands within half a bucket, not on target
     (bucket SELECTION still works, ISO-8601 sorts lexically). Either fold
     to timestamp AND widen the date arm (PG text parity), or fold to a
     date-spelled literal (histogram parity, diverges from PG's text).
- **K88 — folding CAN change ANSWERS; my "estimates only" claim was
  wrong.** `evalArith`'s numeric path is float64-based, so `0.05 + 0.01`
  folds to `0.060000000000000005`, not `0.06`. Q6 survives only because
  that perturbation LOOSENS an upper bound; a fold that tightened one
  would drop rows. PG uses exact `numeric` — file as its own defect.
- **Q6 is a NAMED GATE ITEM, not left to the sweep.** It is one of only
  two matches goopg has and it is in the blast radius of BOTH steps
  independently (a `date + interval` site AND `0.05 +/- 0.01` constant
  arithmetic).


### R44 step A LANDED (`r44-const-fold-before-selectivity/REPORT-stepA.md`)

`foldQualConstants` applies the existing `FoldConstants` to a resolved qual
at the point PG's `preprocess_expression` runs `eval_const_expressions` —
before the estimator. Wired at three sites: both WHERE arms AND
`planJoinPredicate`'s ON clause (the last is REQUIRED per K86, or the round
is WHERE-only).

**Result: net -6 TPC-DS parity categories** (join-method 71->69,
parameterisation 39->37, aggregation-strategy 84->82), **31 plans
changed**, TPC-DS runtime 811s->771s (-4.9%) with Q38 12s->2s, Q87
11s->2s, Q99 5s->1s. Sweep **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0** with all 99 verdicts and row counts identical. TPC-H
byte-identical in both values and plan text. Match unmoved (0/99, 2/22).

- **K89 — I reported "step A changed nothing" from TPC-H evidence alone,
  and that was WRONG.** TPC-H really is byte-identical; TPC-DS moves 31
  plans and -6 categories. This also settles K85 in the REVIEW's favour:
  rev 1 called step A inert, review said the converse was false because
  plain numeric arithmetic already folds, TPC-H made review look wrong,
  TPC-DS showed it was right. **Two corpora can give opposite answers —
  measure BOTH before characterising a change.**
- **K88 confirmed in the wild.** Q6's filter now renders
  `l_discount <= 0.060000000000000005` — `evalArith`'s numeric path is
  float64 (`ParseFloat`/`FormatFloat`), where PG uses exact `numeric` and
  renders `0.06`. Pre-existing (the late fold already rendered it), but
  step A now feeds that string to the ESTIMATOR too. Answer-safe here only
  because it LOOSENS an upper bound. **Filed as its own defect** and it is
  also a live `rendering` divergence.
- **Step B (the temporal domain, K83's 11.9x Q14 error) is NOT landed**,
  and implementing step A confirmed the §5a blockers: the leaf
  `internal/utils/adt/datetime` has formatting/normalisation/validation but
  **no date+interval arithmetic** — that is `executor/expr.go`'s
  `addDateTimeInt`, and executor imports optimizer in ~91 files so the
  dependency cannot be reversed. Step B needs that arithmetic EXTRACTED
  into the leaf package. Plus: still no reachable `provolatile` index, and
  the folded-literal spelling (timestamp vs date) is still undecided.

- **K90 — two operational traps hit this session.**
  1. `/tmp/pp2` (private bench clone + ALL captures) did not survive the
     session boundary; only git-tracked captures under `r0-baseline/` and
     `r2-instrument/` did. **Anything needed across sessions must live in
     the repo.**
  2. The sweep writes plan sections as `===== Qn =====` while
     `pg-plan-parity-diff.py` expects `=== Qn`. Feeding it the sweep file
     directly returns `queries=0 match=0` — which reads exactly like a
     clean run rather than a parse failure. Convert with
     `sed -E 's/^===== (Q[0-9]+) =====$/=== \1/'` first.


### R44 step B LANDED + a correction to step A (`REPORT-stepB.md`)

Q14's 11.9x error is FIXED: `lineitem` 938,645 -> **78,680**, and the
filter now renders `'1995-10-01 00:00:00'::timestamp` — PG's own spelling.
**R44 total: -10 TPC-DS categories and -3 TPC-H categories** (TPC-DS
join-method 71->66, parameterisation 39->35, aggregation-strategy 84->82;
TPC-H parallelism 18->17, qual-placement 6->5, sort-strategy 13->12).
Sweep PASS=95 all-zero, row counts identical, values identical. Match
unmoved (0/99, 2/22).

- **DESIGN §5a.2 RETRACTED — step B needed no executor extraction.** The
  pieces were already importable: `parser.ParseIntervalBodyWithDefault`
  for the interval body, `time.AddDate` for the month/day carry (the same
  primitive `addTimeInterval` uses, so planner and executor cannot
  disagree), and leaf `datetime.FormatTimestamp` for rendering. The
  companion change widens `numericValue`'s `date` arm to accept timestamp
  spellings (keeping the DAY scale), without which `bucketFraction` falls
  back to a flat 0.5 and the fold lands half a bucket off.
- Volatility still unresolved, so the fold is scoped to ONE
  oracle-verified-immutable family (`date_pl_interval`, `provolatile='i'`)
  with both operands literal. Broadening REQUIRES the volatility map.
- **K88 hit for real.** The first version folded `interval 'infinity'`
  arithmetically and returned `119521-07-18 06:23:01.689343` for
  `timestamp '2020-01-01' + interval 'infinity'` — a WRONG ANSWER. The
  executor implements ±infinity as sentinels. Caught by
  TestTimestampIntervalInfinity / TestIsFiniteInfinity /
  TestTimestampSubInfinity — by the SUITE, not by review. Fold now
  declines on `parser.IntervalNoEnd*`/`IntervalNoBegin*`.

- **K91 — MEASUREMENT-INTEGRITY FAILURE; `REPORT-stepA.md`'s "TPC-H
  byte-identical" claim was WRONG and is now corrected in place.** Two
  harness faults, both mine, both introduced after `/tmp` was cleared:
  1. **`launch.sh` lost its serving-binary verification.** I rewrote it
     from memory and dropped the inode check. `<bin> stop -D <dir>` fails
     when `postmaster.pid` is gone or the binary differs, so a **stale
     server kept serving** and every "A/B" ran one binary. One arm even
     named `tmp/goopg-base`, which never existed.
  2. **`capture-tpch.sh` reads `/tmp/parity-r0/queries/tpch`**, wiped with
     `/tmp`. Every capture became a 4-line stub ending `(capture failed)`,
     and **diffing two stubs reports "identical"**.
  Both faults share one signature — **the null result and the broken
  result are indistinguishable** — which is also K90's `queries=0 match=0`
  parse failure. TPC-DS numbers were never affected
  (`tpcds-sf05-regression.sh` fingerprints its own binary).
  **Fixed:** `launch.sh` now kills the port holder by PID and REFUSES to
  proceed unless `/proc/<pid>/exe` matches the requested binary's inode;
  the TPC-H corpus was restored from `tmp/take4/`.
  **Standing rule: a harness must fail loudly, and an A/B must prove which
  binary answered. Where it cannot, the result is not evidence.**


### K92 — R43's TRUTHFUL verdict NARROWED; Q14's last category needs a real feature

After R44, **Q14 differs from PG on `parallelism` alone** (Q1 is next at
`[sort-strategy, parallelism]`), so it is the cheapest candidate third
match. Investigating that route produced a correction to R43.

R43 rev 2 concluded "TRUTHFUL" because goopg's cooperative build
partitions the BUILD relation's scan. That fact stands, but it does NOT
license emitting PG's shape. `Parallel Hash` asserts a **partial inner
path consumed by the Gather's own worker set** — and goopg explicitly
does not do that. `parallel_scan.go`'s `joinOp` arm:

> "P8. Only PROBE side partial: build side drained once by leader before
> fan-out. Attaching allocator build side instead would give each worker
> PARTITION build input, every worker's hash table missing most rows,
> join would silently drop matches."

`parallel.go`'s `stampParallelScan` mirrors it (probe side only, with a
SIBLING WARNING that label walk / `drivingScan` / `attachParallelScan`
must never disagree).

| | who scans the build relation | when |
|---|---|---|
| PG `Parallel Hash` | the Gather's workers, into shared DSM | during the join, behind a barrier |
| goopg | the LEADER's producer goroutines | in `gatherOp.Open`, BEFORE fan-out |

Both parallelise the build scan; only PG's is a partial path under the
Gather. **Stamping `Parallel Seq Scan on part` inside Q14's Gather subtree
would claim the Gather's workers each read a partition of `part` — the
exact arrangement the executor says "would silently drop matches". That is
a misdescription, i.e. the arbitrary plan-forcing the goal forbids.**

**So Q14's third match is NOT cheap.** It requires implementing PG's
execution model (workers building a shared hash from a partial inner), not
relabelling the leader-prebuild model. R43 §6 step 2 must not be
implemented as a labelling change; DESIGN rev 4 §4a records this.


### Post-R44 triage: three measured findings (K93-K95)

Measured while looking for the next round after K92 closed the cheap Q14
route. All three are negative or cautionary results — recorded so nobody
spends a round rediscovering them.

- **K93 — K88 (float64 fold) is a FIDELITY item, NOT a parity lever.**
  Measured: the `0.060000000000000005` artifact occurs **once** in TPC-H
  (Q6) and **once** in TPC-DS. PG's own plans contain **4** such long
  decimals. It does not drive the `rendering` category (32 on TPC-DS), and
  Q6 MATCHES despite carrying it, because the differ compares qual
  literals by column+operator multiset (N6), not by value. Fix it for
  correctness and answer-safety — it remains a real
  planner-vs-PG-`numeric` divergence — but do NOT schedule it expecting
  category movement.

- **K94 — Q1's gap is a COSTING divergence, not a disabled capability.**
  Q1 is the nearest miss after Q14 (`[sort-strategy, parallelism]`). PG
  sorts INSIDE the workers and finalises ordered:
  `Finalize GroupAggregate <- Gather Merge <- Sort <- Partial HashAggregate`.
  goopg gathers unsorted and sorts at the top:
  `Sort <- Finalize HashAggregate <- Gather <- Partial HashAggregate`.
  `GatherMerge` EXISTS in the planner, and there is a knob for the
  decision — but **`GOOPG_PARTIAL_SORT_PATHS=on` leaves Q1's plan
  byte-identical**, so the priced tournament still picks goopg's arm. This
  is a cost-model divergence to be won on cost, not a flag to flip.

- **K95 — parallel row estimates diverge in BOTH directions; no single
  convention explains it.** A tempting hypothesis (goopg renders TOTAL
  rows on a Parallel Seq Scan where PG renders PER-WORKER) fits two data
  points and is REFUTED by the third:

  | query | goopg | PG | ratio |
  |---|---|---|---|
  | Q1 | 5,916,028 | 1,479,529 | **3.99** |
  | Q14 | 78,680 | 18,444 | **4.26** |
  | Q6 | 1,506 | 28,092 | **0.05** |

  Q6 is ~19x LOW where the others are ~4x HIGH. So estimate parity has at
  least two independent root causes on the SAME column of the SAME table,
  and any "fix the parallel divisor" round would be chasing one of them
  while the other stays. Instrument per query before generalising.

- **Caveat on R44 that honesty requires recording: it improved shape
  categories while moving at least one estimate FURTHER from PG.** Q6's
  `lineitem` estimate went 2,412 -> 1,506 against PG's 28,092. Q6 still
  MATCHES (shape is unaffected), but R44's stated aim was estimate parity,
  and on this query it went the wrong way.


## R45 — split aggregate + ordered gather cannot combine (K96, DESIGN pre-review)

`r45-gather-merge-ordered-finalize/DESIGN.md`. Found by pursuing K94 (Q1's
"costing" gap) to its structural cause — which turned out **not to be
costing at all**.

| node | goopg TPC-H | goopg TPC-DS | PG TPC-H | PG TPC-DS |
|---|---|---|---|---|
| `Finalize GroupAggregate` | **0** | **0** | 5 | 18 |
| `Gather Merge` | **0** | 3 | 9 | **85** |

- **K96 — the shape PG picks is UNREACHABLE, so no cost setting could
  select it.** `rebuildWithGather` (`parallel.go`) switches on
  *mutually exclusive* arms — `case tgt.mergeKeys != nil` (target is a
  Sort) -> `NewGatherMerge`, versus `case tgt.splitAgg` (target is an
  Aggregate) -> `splitAggregate`. And `splitAggregate` (`parallel.go:1059`)
  **hardcodes `NewGather` at :1067** — never `NewGatherMerge` — and copies
  the ORIGINAL aggregate's strategy to the Finalize. Two consequences,
  both confirmed by the 0/0 measurement: a split aggregate always gets an
  UNORDERED gather (so `Gather Merge` only arises from the other arm,
  hence goopg's 3), and the Finalize inherits a hash strategy, so
  **`Finalize GroupAggregate` is unreachable by construction**.
- **This CORRECTS K94.** K94 said Q1's gap was "a costing divergence,
  because `GOOPG_PARTIAL_SORT_PATHS=on` leaves the plan byte-identical".
  The observation was right, the conclusion premature: the alternative
  shape does not exist to be priced. K94's framing becomes true only
  AFTER R45 makes the shape reachable.
- PG's Q1 needs both arms at once:
  `Finalize GroupAggregate <- Gather Merge <- Sort <- Partial HashAggregate`
  (sort INSIDE the workers, merge order-preservingly, finalise ordered)
  versus goopg's
  `Sort <- Finalize HashAggregate <- Gather <- Partial HashAggregate`.
- **Not a new executor feature** (unlike K92's Parallel Hash):
  `operators_gather_merge.go` exists and the 3 TPC-DS occurrences exercise
  it. The missing piece is the planner COMBINATION.
- **Widest surface of any round this session** — it moves every parallel
  aggregate in both corpora. `splitAggregate`'s own comment records that
  this pass runs on plans "the process-wide cache may be handing to other
  sessions right now", so the non-mutating shallow-copy discipline is
  load-bearing.
- Gate note carried forward from K91: the TPC-H A/B **must** verify the
  serving binary by inode. That check's absence produced a false result
  this session; it is not boilerplate.

### R45 REJECTED on review — the fix is architecturally impossible (K97)

Review VERIFIED §1's measurement and §2's structural cause, then refuted
the FIX at its root. **K96's diagnosis stands; K96's proposed remedy does
not.**

- **goopg's Partial aggregate emits ZERO rows.** It publishes transition
  states through a SIDE CHANNEL, not the plan tree —
  `parallel_agg_split.go` says so in terms: *"They do not travel through
  Gather at all."* `AggModePartial` merges into `aggPartialAccum` and sets
  `o.rows = nil`; `AggModeFinal` rebuilds from `accum.order`, i.e.
  **worker-arrival order**. So a `Sort` between Partial and Gather would
  sort an EMPTY stream and `Gather Merge` would merge EMPTY streams — a
  plan that RENDERS like PG's while executing today's semantics. That is
  exactly the arbitrary plan-forcing the goal forbids.
- **Worse than cosmetic: a wrong-answer hazard.** The only point of the
  shape is to drop the top-level `Sort`, which needs pathkeys claimed on
  the Finalize — whose order is `accum.order`. **Q1 would return unordered
  rows.**
- **A trap for whoever tries this next:** `aggregateOp` already sorts its
  output by group-key columns for determinism, including on the Finalize
  path, so Q1 might APPEAR correct after dropping the Sort. That sort uses
  collation 0, fixed ASC, fixed NULL ordering — it cannot serve `DESC`,
  `NULLS FIRST/LAST`, non-default collations, or ordering by aggregate
  outputs. **Do not mistake incidental ordering for a pathkey.**
- **`Finalize GroupAggregate` is unreachable for a SECOND, independent
  reason**, and R45 §4's "not a new executor feature" was FALSE: sorted
  aggregation is gated to `Mode == AggModeSimple`
  (`operators_join_agg.go:2222`), so a Finalize marked `AggStrategySorted`
  falls through to the HASH path. Marking it would print
  `Finalize GroupAggregate` over a hash table — a label lie
  `operators_explain.go`'s own comment exists to prevent.
- Correction to K96's wording: the Finalize is hashed not because it
  *copies* the strategy but because **the split arm never OFFERS
  `AggStrategySorted`** (`partialaggupper.go`).
- **Prior art R45 failed to find:** `partialaggupper.go:352-359` already
  reasons that presortedness cannot survive goopg's Gather, and
  `parallel_agg_split.go` records the rejected alternatives (a
  pointer-bearing Datum kind; a side channel threaded through
  `rowBatch`/`TupleSlot`). This ground was surveyed before. **Search prior
  design notes for the MECHANISM, not just the symptom, before designing.**
- **The real item** is PG's `AGGSPLIT_INITIAL_SERIAL` /
  `FINAL_DESERIAL` — row-borne partial aggregate states with
  per-aggregate serialize/deserialize, plus `openSorted` extended to
  `AggModeFinal`. A multi-round EXECUTOR programme, comparable to or
  larger than K92's Parallel Hash. Only after it can Q1's shape be PRICED
  rather than rendered.

## R46 — cost the legacy funnel's index-vs-seq choice (design approved, implementing)

`r46-legacy-index-seq-competition/DESIGN.md` (reviewed 2026-09-10,
APPROVE-WITH-NOTES, notes applied; design commit `ffab1708d`).
Baseline at HEAD on canonical data (pinned env): TPC-H `match=1
shapediff=19`, TPC-DS `match=0 shapediff=70`; R43's `match=2`/Q6-match
unreproduced (recorded as discrepancy). Oracle note: TPC-H fixtures
are all-serial (owner decision needed for re-capture).
**K98:** `planIndexScanFromWhereShape` equality arm returns
`IndexScan` unconditionally; TPC-DS Q9 differs on `scan-type` ALONE
(`reason`, 1 page/35 rows) — nearest miss in either corpus. Change:
equality-arm-only cost competition (`costIndexScanCore` vs
`costSeqscan`, per-arm inputs table in DESIGN) with seq-wins
decline; correlated/range/SAOP untouched. Tests: Q9→MATCH, zero
EXTRA flips, unit pins both directions first.

## R46 result (2026-09-10)

`r46-legacy-index-seq-competition/REPORT.md`. IMPLEMENTED, all
gates pass. Two producers gated (funnel equality arm + absorber
`eqKey` rewrite — the second found live: funnel verdict=true yet
EXPLAIN still indexed). **TPC-DS Q9 → MATCH** (first TPC-DS match
of the programme); scan-type 74→73; TPC-H categories identical.
Gates: units + suites (5 new pins) + spotcheck + SF0.5 sweep
(PASS=95, MISMATCH=0; plan-shape only Q9) + A/B both corpora (only
Q9 moves).
- **K100 (new, executor).** Sequential reg*[] comparison broken
  twice over (scalar-cast leak drops IsArray; OID-vs-name compare
  without catalog) — R46 carve-out keeps the index there, dies
  with K100. Corpus impact zero.
- Anomaly on record: one build produced corpus-wide width shifts
  (undetermined; clean rebuild reproducible — verify serving
  behavior, not just inode); one test passed 4× then failed 20/20
  on the same tree (suspected stale binary in stash-pop window).

## R47 — Expose grouping candidates; measure at ordered level (rev 3 APPROVED-WITH-NOTES, implementing)

`r47-q4-upper-rel/DESIGN.md`. Rev 1 REJECTED (11 findings); rev 2
REJECTED (narrow); rev 3 APPROVED-WITH-NOTES 2026-09-10 (notes
applied). Restructured per reviews: NO predicted flip, NO
invented pick rule — safe plumbing (slice 1, byte-identical
gate) + measurement (slice 2), corpus arbitrates. Step 0 closed:
semi legacy-unstamped, 1141.32 = wrapper double-perRow (exact),
width flip partially answered (verdict-neutral), five PG flips
re-measured live and archived in `pg-flips/`. Firing micro-rule
honestly unidentified (all constructed models predict hashed at
least once); flips arbitrate, STOP binds. K9-compliant: no
parity-verdict claim (fixture = movement detector only).
Slices: (1) per-candidate Agg clone, Paths-not-Nodes,
pointer-identical copy-back, Memoize census; (2) new
ordered-level loop over survivors via existing addOrderedPaths
+ add_path/setCheapest (M0129-S1 + sort-disabled declared).
Tests: slice gates, comparator-parity pins, dominance
instrumentation, characterization flips (informative),
pre-declared estimate/Memoize/inode pins, standard gates.
Follow-ups: R48 (placement), firing-rule (hypothesis-eliminating
census), K100, fixture re-capture (owner).

## R47 slice 1 result (2026-09-10, verified on resume)

Per-candidate Agg spec clones (`groupingpaths.go` 3 sites,
`partialaggupper.go` 5 sites) + 2 dominance TDD pins. Re-verified
on resume after handover: `go build` clean, both pins PASS, full
`internal/optimizer` suite green, units gate green, pgbench smoke
green (0 failed). Serving behavior (K91): fresh `:5554` launch of
the current-tree binary on the TPC-H clone — full corpus capture
byte-identical to the R46 baseline (`oc-tpch-r46c.txt`) on all 22
queries (Q15a helper file absent at capture time; manual Q15a probe
matches baseline exactly incl. costs/widths/filter rendering), and
the peer's TPC-DS slice-1 capture differs from its baseline only in
header + psql-PID noise. Q4 before-shape confirmed live (hashed:
HashAggregate startup 1283.98, semi 1141.32/57066/width 448).
Slice 2 (translation helper + ordered loop + copy-back) NOT started.

*Rev history: rev 1 REJECTED (F1-F11: fuzz-site error, missing
decision site, NLI misattribution, unestablished seed, missing
rows, self-contradiction, K9 breach, open blast radius/guards);
rev 2 REJECTED (narrow: slice-1 interface, unnamed loop,
M0129-S1/sort-disabled disclosure, ungrounded flips, pins,
K9 framing). All items closed in rev 3 per the two review
verdicts; full text in the review reports summarized above —
kept for the record, not repeated.*

## R47 slice 2 plan (2026-09-10, code study complete — review then implement)

New file `internal/optimizer/upperorderedgrouping.go` + pins in
`upperordered_test.go`, per `r47-q4-upper-rel/SLICE2.md` (written
2026-09-10 from live-tree code study; agent-reviewed before
implementation commit). Translation helper (sorted-candidate
emission order → output-coord pathkeys; executor guard mirrored;
PathSort-child + full positional group run + name-verified group
prefix required; hashed/index/presorted/expression-key fall out
via the checks) + ordered loop at the planSelect normal ORDER BY
arm only (gates pre-mutation: agg != nil, node == agg.node,
selectSrfPending == nil, ≥2 PathAgg + 0 PathFinalizeAgg on the
re-fetched GROUP_AGG rel; per-candidate shallow copy with
translated pathkeys through existing addOrderedPaths +
setCheapest/getCheapestFractionalPath; winner built via
createPlanNode, copy-back descending through *Sort only +
stampAggregateInputTarget re-run; existing orderSort stamp code
reused (control flows through the shared block, no early return);
loop elects ⇒ normal createOrderedPaths call skipped; decline ⇒
snapshot/restore Pathlist+cheapest (review-adopted).
Prediction sharpened on review: GROUPING still picks hashed, but
the ORDERED election is EXPECTED to flip Q4 to no-sort sorted
(the slice-1 pin elects it at Q4's numbers 70122-vs-69911 via
fuzz+tie-break) IF production matches the pin — flip = stretch
recorded, no flip = census must say where production diverged.
Pass = gates + ZERO EXTRA flips + hypothesis-eliminating §3.3
census.
Recipe mapping notes (handover §3 mapped, not coded): "child must
be Sort" = PathSort *Path* (losers stay unbuilt); presorted/index
→ nil subsumed by child-kind + run checks (+ explicit
GroupKeyOrder decline); DisabledNodes propagate via
disabledNodesFor inheritance (verified); sizing from aggNode ≡
normal call (verified).

## R47 slice 2 result (2026-09-10, MEASURED — 16 flips toward PG, ZERO EXTRA)

Slice-2 loop landed (`upperorderedgrouping.go` new + `planner.go`
normal-arm loop-first + 6 TDD pins incl. post-decline byte-identity,
all green). Full measurement in `r47-q4-upper-rel/REPORT.md`.

- TPC-H (vs byte-identical slice-1): Q7 + Q8 flip `Sort →
  HashAggregate` to `GroupAggregate → Sort(input)` (PG: GroupAggregate
  both); Q4 does NOT flip — loop ran, hashed+Sort 1426.78 beats
  no-sort sorted 6077.69 (dominance). Pin's near-tie numbers
  (70122/69911) turned out PG-oracle-scale (PG Q4 Finalize
  GroupAggregate 70094.27..70122.64, semi-out 3439 rows), NOT
  goopg-scale production (57066 rows) — pin stays a unit probe.
- TPC-DS SF0.5 (vs s1 ≡ r46c): 14 flips, all `Sort → HashAggregate`
  to `GroupAggregate → Sort(input)`, PG uses (Finalize)
  GroupAggregate/Group at every station (Q37/Q82: PG `Group` node —
  goopg has no Group; GroupAggregate is the strategy match).
- Q7/Q8 census: gathered-hashed+Sort vs gathered-sorted-as-is tie to
  the penny on total; startup tie-break elects no-sort = PG's choice.
  Split was dominance-pruned at grouping before the loop (moot gate).
- Surfaced (not created) skew: `costAgg` charges per-row hashing,
  `createAggPlan` display omits it — loop-elected Sorts price from
  path cost while children keep node display (6+16 cost-only
  Sort/Limit lines). Unification = separate R-item (filed, NOT
  slice-2 scope). All loop elections path-vs-path, PG-faithful.
- Gates: units + pre-commit green; spotcheck Q12=2/Q13=34 PASS;
  SF0.5 sweep PASS=95 MISMATCH=0 SKIP=4, plan-diff channel exactly
  the 30 adjudicated queries. Match count not a criterion; firing
  micro-rule still UNIDENTIFIED (honesty preserved); Q4 gap localized
  to row estimates below the agg (57066 vs PG 3439).

## R48 — semi JoinQual placement + `Filter: (true)` drop (design; Step 0 closed 2026-09-10)

`r48-semi-joinqual-placement/DESIGN.md` (agent-reviewed
APPROVE-WITH-NOTES 2026-09-10; F1+F2 blockers + F3-F10 notes all
closed in text). Named by R47 DESIGN §4 ("semi JoinQual placement
+ `Filter: (true)` drop"). Step-0 pair is TPC-H Q4, captured live
2026-09-10 (PG :65432 vs s2 tree, GUCs pinned `work_mem='64MB'`,
`max_parallel_workers_per_gather=4`), archived in-tree as
`r48-semi-joinqual-placement/pg-q4.txt` +
`goopg-q4-s2.txt` (R47 pg-flips/ precedent; review F1):

- PG: `Nested Loop Semi Join` with NO join-level qual; the EXISTS
  inner qual sits on the inner probe —
  `Index Scan ... Index Cond: (l_orderkey = orders.o_orderkey)`
  `Filter: (l_commitdate < l_receiptdate)`.
- goopg s2: same join shape, but the qual stays at join level
  (`Filter: (l_commitdate < l_receiptdate)`) under a
  `*NestedLoopIndexJoin` whose inner `Index Scan` carries no
  `Filter:` — plus a stray second line, `Filter: (true)`.

Two independent halves (either order; Filter-half first, de-risks
the census):

1. **`Filter: (true)` drop (EXPLAIN-only).** Census on the s2
   corpora: 6 TPC-H + 34 TPC-DS stray lines, every one an
   attached-`Filter` (collapsed wrapper) with a trivially-true
   predicate above a join/NLI — scaffolding the unnest passes
   leave behind (`unnest.go:414` sets `BooleanConst{true}`
   instead of removing the wrapper; `:1611`, `:3157`, `:3281`,
   `:4309` keep `Filter`-wrapping-with-true so downstream
   recursion "still finds the join"). `combineAnd([])` is nil,
   so no `Filter: (true)` comes from an empty residual — all 40
   are wrappers. Fix at the renderer (`walkPlanFiltered`: skip a
   trivially-true `*Filter`, carry nothing down) — zero
   executor/estimate impact; only the stray lines vanish (their
   host lines re-price from the wrapper to the child, which is
   the PG-faithful carrier).
2. **Semi JoinQual placement (planner).** Q4's NLI residual
   (`l_commitdate < l_receiptdate`, inner-only) belongs on the
   inner probe as `IndexScan.Cond` — the channel the struct doc
   (`plan.go`, `IndexScan.Cond`) built for exactly this ("Only
   the NLI arm sets it"; evaluated per heap tuple the probe
   returns; rendered as the scan's `Filter:`, PG-identical).
   The legacy `*Join → NLI` rewrite (`tryBuildNLI`,
   `nl_index_join.go:318`) hoists inner Filters into
   `residualPred` instead of lowering inner-only conjuncts to
   `is.Cond` (leaf-local shift, SEMI/ANTI only — LEFT keeps its
   residual: a moved qual would stop filtering null-extended
   rows). Converges legacy with the path arm (rule #2) and with
   PG's `distribute_restrictinfo_to_rels` (MOVE, not goopg's
   usual copy). Executor support already exists (fused arm
   relies on per-probe Cond eval); values gates arbitrate.
   Out of scope: INNER/LEFT NLI residuals, plain-`*Join`
   residuals, `BitmapHeapScan.Cond` inner (same mechanism,
   later slice if census implicates).

Pass = stray-line census 40 → 0 with no other EXPLAIN line
moving except host-line re-pricing + the NLI-residual lines
that move onto inner scans (each adjudicated toward PG's
placement); ZERO shape flips required, ZERO EXTRA allowed;
values gates (TPC-H digest 24/24, SF0.5 sweep all-zero) bind.

**Result — LANDED 2026-09-10** (report:
`r48-semi-joinqual-placement/REPORT.md`). Half-1: `Filter:
(true)` skip in BOTH Filter arms of `operators_explain.go`
(plain + ANALYZE twin — the design named only the plain arm;
the twin carries the same arm per the file's sibling-agreement
doctrine). Half-2: `lowerSemiResidualToCond` in
`nl_index_join.go`, SEMI/ANTI-gated, runs BEFORE
`indexOnlyNLIInner` (F2 order pin — IOS sees `Cond` set and
declines). Census: strays 6 → 0 TPC-H / 34 → 0 TPC-DS,
normalized shape diffs empty (ZERO EXTRA flips); Half-2 moves
= 9 TPC-H lines, all adjudicated (Q4 join-qual → inner probe
`Filter:` = PG placement; Q21 outer Anti mixed residual splits
— outer half stays on the join, inner half to the `l3` probe),
TPC-DS byte-identical (strict no-op). Q4 semi core now
placement-identical to `pg-q4.txt` (remaining gap = recorded
F9 parallel-shape expectation). Values: digest 24/24 MATCH
VERDICT PASS; SF0.5 sweep PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0; spotcheck Q12=2/Q13=34 PASS; optimizer+executor
suites + pre-commit units green. Fossil-carrier finding
(restated): the true-wrapper is fully costed, predicate
swapped post-costing with stored PlanCost kept — Q2 host rows
5 → 160000, PG-EXACT vs the live oracle. Pre-existing, not
owned: `TestLeftJoinCrossRelationResidualReachesNLI` SKIP
(identical at clean HEAD — R25 arm serves that shape first);
`Join Filter: (true)` (`exists_to_any.go:355-367`) still
corpus-zero, follow-up stands.

## R49 — parameterize the bitmap-heap NLI probe (design LANDED 2cbc83f06; Slice A LANDED 521bc82 2026-09-10 — report `r49-bitmap-probe-param/SLICE-A.md`; Slice B IN PROGRESS (setup done 2026-09-10, worktree /tmp/wt-r49b). Step 0 closed 2026-09-10; agent review APPROVE-WITH-NOTES 2026-09-10, 1 merge blocker + 8 notes, all applied; impl LANDED a16db55 2026-09-10; pins LANDED 3639208 2026-09-10 (OP1-3 MOVE-contract + SEMI/keyless, deform widening, e2e doll-house: shape/lossy/NULL/composite/LEFT/cond+lookupBounds; executor+optimizer suites green). NEXT: Slice-B gates — DONE 2026-09-10, report f60d69bc1 (census moves-only TPC-H 14/DS 40, digest 24/24 Q12=2/Q13=34, SF0.5 95 PASS/0 mismatch, sibling audit clean). Slice B COMPLETE.)

Named by R48 DESIGN §4 ("IOS/bitmap-Cond inners ... (their
double-eval is a separate R)"). Census on the post-R48 corpora
(`oc-tpch-r48h2.txt`, `oc-ds05-r48h2.txt`): every `Filter:` on a
`Nested Loop` whose inner is a `Bitmap Heap Scan` is an NLI
(`NestedLoopIndexJoin.Predicate` renders as `Filter:`, while
plain-`*Join` renders `Join Filter:`) carrying the probe clause
at the join, over an UNPARAMETERIZED bitmap (0/40 TPC-DS
`Bitmap Index Scan`s carry an `Index Cond:`; TPC-H likewise) —
TPC-H Q2(4)/Q5/Q8(2)/Q11(4)/Q20; TPC-DS Q3/Q19/Q21/Q30
(ctr_customer_sk)/Q32/Q37/Q39(×2)/Q40/Q42/Q49(×2)/Q52/Q53/
Q55/Q61(×2)/Q63/Q64/Q75(×2)/Q76(ws_item_sk)/Q80(×2)/Q81
(ctr_customer_sk)/Q82/Q89/Q98 — 28 lines, inner-child mapping
verified per line (inner = `Bitmap Index Scan on *_pkey`;
mapping table archived with the Slice-A census). Non-bitmap
`Filter:` lines (TPC-DS Q1/Q6/Q18/Q30a/Q34/Q44/Q46/Q54/Q68/
Q71/Q72/Q73/Q76a/Q79/Q81a — CTE/Hash/Merge/Seq inners,
SubPlan/InitPlan quals) are out of scope. Step-0 pair is
TPC-DS Q3, captured live 2026-09-10
(PG :65438/tpcds05 vs r48 corpus, GUCs pinned `work_mem='64MB'`,
`max_parallel_workers_per_gather=4`), archived in-tree as
`r49-bitmap-probe-param/pg-dsq3.txt` +
`goopg-dsq3-r48.txt`, plus `pg-q5.txt` + `goopg-q5-r48.txt`
as the shape-divergence witness (PG hash-joins supplier;
goopg nestloops with a bitmap inner — parameterizing the
probe is PG-ward but NOT PG-identical there):

- PG: `Nested Loop` with NO join-level line; inner
  `Bitmap Heap Scan ... Recheck Cond: (ss_item_sk =
  item.i_item_sk)` + `Bitmap Index Scan ... Index Cond:
  (ss_item_sk = item.i_item_sk)` (outer ref as parameter).
- goopg r48: same join shape, but the probe clause stays at
  join level (`Filter: (item.i_item_sk =
  store_sales.ss_item_sk)`) over a key-less
  `Bitmap Index Scan on store_sales_pkey` (no `Index Cond:`,
  no `Recheck Cond:`).

Mechanism (surveyed, not yet designed): the NLI-bitmap path
arm (`createNestLoopBitmapJoinPlan`, `createplannl.go:454`)
binds probe keys per outer row already (`bis.Key`, executor
`BindOuter`/`Rescan` plumbing exists) but deliberately clears
`BitmapQual` ("no leaf-local form of = <outer key>") and
folds the probe clauses into the join Predicate (OP1-3 guard
test pins this: lossy-page recheck rides the Predicate).
Two slices; Slice A first (EXPLAIN-only, de-risks the census):
(A) render bound probe keys as `Index Cond:` via
`formatIndexCondParts` (NOT `bis.Pred` — SEARCH coordinates),
(B) keep `BitmapQual` in merged outer++inner coords + retain
the outer slot in the heap op for combined-row recheck eval,
and DROP the folded clauses from the Predicate (MOVE, not
copy — R48 doctrine) — trading always-recheck for PG's
exact-probe + lossy-recheck model. Slice-B merge blocker:
NULL probe keys currently full-scan (`lookupKey(s)` NULL →
`(nil,nil,nil)` → open-ended `RangeScanWithPos`; sibling
index arm returns `ok=false`) — Slice B must return an empty
TBM, with NULL tests in both shapes (single-column full-key
+ composite prefix). Values gates arbitrate; lossy-page tests
must prove the recheck still fires per outer row.

Out of scope: plain-`*Join` `Join Filter:` residuals (Q7/Q13/
Q14/Q15/Q17/Q25/... — separate R per R48 §4); semi/anti over
non-scan inners (Q21, Q22 — PG comparison in DESIGN; likely
already PG-shaped); Q17/Q6/Q30 SubPlan/InitPlan shapes;
LEFT (Q72 — null-safety, same argument as R48); INNER
non-bitmap NLI residuals (deferred per R48).

Pass = every in-scope join-level probe `Filter:` moved onto
its probe (`Recheck Cond:` + `Index Cond:`, no join line),
each adjudicated toward its PG counterpart (Q5-class shape
divergences recorded, not forced); ZERO EXTRA flips;
values gates (TPC-H digest 24/24, SF0.5 sweep all-zero) bind.

Slice-B setup 2026-09-10 (worktree /tmp/wt-r49b, probe
`internal/executor/zz_probe_bitmap_test.go`, throwaway): live
NLI-bitmap e2e recipe PROVEN — 20k-row inner / 4 keys + 4-row
outer + WHERE (filterless INNER never reaches the search:
`joinTreeHasOuterLink` false → legacy path) wins
`Nested Loop` → `Bitmap Heap Scan` + `Bitmap Index Scan` on
NATURAL costs (no toggles; bitmap probe 262 < index probe
947) and executes 20000/20000 rows. Forcing notes: catalog
wrapper + `EnableIndexScan=false` do NOT force it — the
decomposed legacy shape ignores both, and the search joinrel
ctor drops inner `DisabledNodes`; the base tournament drops a
bitmap probe unless per-probe cost clears the full-seq
prebuilt seed (2k rows: 33 vs 30 dropped; 20k: 262 vs 298
kept — the index sibling survives via its pathkeys axis).
Full mechanism notes → `SLICE-B.md` with the fix.

Slice-B plan 2026-09-10 (`r49-bitmap-probe-param/SLICE-B.md`,
agent review APPROVE-WITH-NOTES 2026-09-10, 9 notes, all applied):
planner BitmapQual-from-pairs
(inner-left, merged coords) + residual-only Predicate;
heap-op outer-slot retention + combined-row evalBitmapQual
branch (outerSlot==nil keeps legacy inner-row eval);
NULL-key empty TBM via lookupBounds flag; all three in ONE
commit (no safe intermediate); pins = OP1-3 update +
lossy-per-outer-row + NULL both shapes + deform superset +
planner e2e + Recheck render.

## R50 — plain-`*Join` `Join Filter:` residuals (setup 2026-09-10; PG adjudication running)

Named by R48 DESIGN §4 ("plain-`*Join` residuals") and R49
DESIGN §4 ("Plain-`*Join` `Join Filter:` residuals ... —
separate R per R48 §4"). Post-R49 census on the gate corpora
(`oc-tpch-r49b.txt`, `oc-ds05-r49b.txt`; machine attribution —
a qual line counts iff its nearest less-indented node is a
join; ZERO orphans): TPC-H 2 `Join Filter:` keys (Q7
`Nested Loop`, disjunctive n_name OR; Q19 `Hash Join`,
brand/container/quantity OR) + 6 NLI-`Filter:` keys
(Q2/Q17/Q20/Q21×2/Q22 — NOT this R, R48/R49 own the
Predicate slot); TPC-DS 38 `Join Filter:` keys / 64 lines
over 36 queries (Q4/Q11/Q13×2/Q14/Q15/Q16/Q17/Q19/Q24/
Q25/Q29/Q31/Q37/Q45/Q46/Q47/Q48×2/Q50/Q54/Q56/Q57/Q58/
Q59/Q60/Q64/Q65/Q68/Q72/Q74/Q75/Q80/Q82/Q83/Q85/Q94/Q95)
+ 12 NLI-`Filter:` keys (SubPlan/InitPlan/LEFT shapes,
out unless the census implicates the same mechanism).
Census-trap on record: the first attribution regex
(`Hash (?:Left|...|Anti )?Join` — trailing space bound
only to the LAST alternative) silently dropped the whole
`Hash Semi Join` family from the key census (lines still
visible in the unfiltered dump, which is what caught it);
fixed pattern keeps the space outside the group, sanity
prints all six join spellings, orphans must read 0.

Step-0 TBD by adjudication: PG sweep running
(`/tmp/pp2/capture-pg-r50.sh` — PG :65432/tpch Q7+Q19, PG
:65438/tpcds05 all 38 affected DS queries, GUCs pinned
`work_mem='64MB'`, `max_parallel_workers_per_gather=4`;
baseline binary `/tmp/pp2/bin/goopg-r50` built from this
worktree, byte-identical to the r49b gate image, `cmp`
clean). Each goopg join-level line gets one PG verdict: (a)
PG-identical Join Filter (no action), (b) PG places it as
Hash/Merge Cond or a pushed-down Filter/Index Cond (MOVE
candidate, R48 doctrine), (c) PG picks a different join
(join-choice — different R, NEVER force the shape).
Mechanism survey + slicing after adjudication; values gates
(TPC-H digest 24/24, SF0.5 sweep all-zero) bind as usual.

Adjudication DONE 2026-09-10 (PG sweep
`/tmp/pp2/pg-r50/`: 2 TPCH + 42 DS plans, GUCs pinned).
(a) PG-identical JF, no action: TPCH Q7 (OR stays JF even
on PG's hash join — non-equi JF is PG-faithful), DS Q13N/
Q48N (giant OR), Q15/Q45 (substr-ANY), Q46/Q68 (city<>),
Q54 (county), Q64, Q85
(PG keeps even equi as JF on NL), Q14 (PG also NL JF on
the outer arms), Q72, Q31/Q75/Q65/Q92 (non-equi, same
role), Q19 (substr<>), Q95 (warehouse<>, same role),
Q16/Q94 (self-ref `x<>x`; PG shape differs entirely).
(c) join-choice, different R: TPCH Q19 (PG NLI probe, no
join line), DS Q17/Q25/Q29/Q50 (ss/sr-customer; PG hashes
or index-probes elsewhere), Q37/Q82 (PG `Index Cond:
(inv_item_sk = item.i_item_sk)` — index-probe R), Q80
(PG HC on hash; goopg NL — join R, NL has no key slot).
(b) SLICE A — hash-`Join Filter:` conjuncts already
covered by the sibling `Hash Cond:` (redundant double-eval;
PG never duplicates — PG Q56/Q59 show HC-only): Q56×3 +
Q60×3 (JF textually == HC, `item_id`), Q47×2 + Q57×2 (JF
category+brand pair ⊂ HC), Q59×1 (store_id ⊂ HC),
Q58×2 (drop the `item_id` conjunct, keep ranges — PG:
MC(item_id)+JF(ranges)), Q24×2 (zip dup exact; PG keeps
JF-on-NL — adjudicated: doctrine over coincident text,
query stays shapediff), Q4×5 + Q11×3 + Q74×3
(customer_id dup/partial + CASE ratio stays; same note).
26 lines / 10 queries. Step-0: DS-Q56 (exact-dup ×3;
goopg HC+JF vs PG HC-only). Safety core: rows reaching JF
eval already satisfy HC (INNER/SEMI), so HC-implied JF
conjuncts are dead — drop is values-neutral by
construction; ANTI explicitly out (no ANTI in scope).
Follow-ups (not this slice): Q13H/Q48H (state/profit OR on
hash — PG placement TBD from full Q13 text), plain-NL equi
probe-ability (Q37-class). (Q83 was filed here as "JF-only
`item_id`, extraction failure" but the Slice-A gate corpus proved
that wrong — same-node HC+identical-JF over char-typed CTE outputs,
PG Q83 zero-JF — so Q83×2 landed IN Slice A; DESIGN §3.)

Slice-A mechanism SURVEYED + DESIGNED 2026-09-10 (design
`r50-hash-joinfilter-dedup/DESIGN.md`, slice plan `SLICE-A.md`, review
agent pending): the dup is pair ∈ HashKeys (HC renders ALL of them,
`operators_explain.go:973`) but ∉ safe (`ExecHashKeyPlan`, Residual =
Predicate minus safe-covered). Census of the discriminator: EVERY
in-scope pair is bpchar-family — character(16) `i_item_id`/`s_store_id`,
character(50) `i_category`/`i_brand`, character(10) `ca_zip`/`s_zip`
(PG :65438 information_schema), or char-typed CTE outputs (Q4/Q11/Q74
`customer_id`, Q47/Q57, Q58 `item_id`). Decisive control: Q47 keeps
character category+brand while subtracting varchar(50)
`s_store_name`/`s_company_name` + numeric `(rn-1)=rn` in the SAME join.
Probe matrix (throwaway `zz_r50_probe_test.go`): char-base DUP,
varchar-base CLEAN, char-CTE DUP, varchar-CTE CLEAN — CTE schemas
propagate types (runtime `Type.Name` for `char(16)` DDL is `"char"`), so
the discriminator is PURELY the whitelist name. Fix (ONE commit, no
split-brain — single predicate feeds renderer residual + executor
encoding): admit `"char"`/`"bpchar"`/`"character"` in
`isHashSafeTypeName` (three spellings, one family — same as text/varchar
and bool/boolean precedent). Safety: width-carrying bpchar stored
TRIMMED (codec `coerceTextLikeDatum`, no padded Datum representation),
so datumKey ≡ `=`; unbounded-verbatim keys differ-but-equal never meet
(same miss as the residual gives today) and key-equal ⟹ byte-identical
⟹ `=`-true; NULL keys never meet; INNER/SEMI only; float + arrays stay
excluded. Non-lead-unsafe (Q47-brand) JOINS the composite key encoding
on admission — no separate enforcement needed. Gates: 26-line census
A/B (HC-extra fails), digest 24/24, SF0.5 all-zero, units green.

## R51 — implied-equality seam switch (LANDED 2026-09-10, report `r51-implied-equalities-seam/REPORT.md`, slice plan `SLICE.md`)

K26's candidate-generation half, landed. One-line switch (`joinsearchseam.go:458`
constants→transitive closure) + 2 PG-adjudicated re-baselines. Blast radius exactly
K26's prediction (pinned-semi PASSES via the kept `nliProbeKeys` fix; 2 Slice3
keep-assertions re-baselined — Q9's new innermost join IS PG's `partsupp⋈part` on
the synthesised clause, F4 pair-rule holds unchanged). Corpus: TPC-H join-method
11→9 (Q5+Q9), agg +1 sideways; TPC-DS four +1 tag side-effects; **join-order
95/18 UNMOVED** — K26 §9.2 confirmed, the costing half (`join_search_one_level`
pricing) is the named next round. Values: digest 24/24, Q12=2/Q13=34, SF0.5
95/0, suites + vet green. Reviewed APPROVE-WITH-NOTES, notes applied
(REPORT.md §7 — C-04a ordering verified, re-baselines sound, executor
risk low; carry-forward: name the 7 DS shape-changed + Q5 agg-sideways
vs PG before the costing round; Q15a-splice / `.norm`-arm provenance
stays attached).

## R52 — R51 shape-movement adjudication (DONE 2026-09-10, report `r52-r51-shape-adjudication/REPORT.md`, docs-only, no code)

R51 review item 1 CLOSED: all 11 headline/shape movers + 12
text-only named vs PG (review APPROVE-WITH-NOTES, notes applied; first
draft missed MISSING-NODE movers Q11/Q47/Q57 — re-derived over ALL
verdicts). H: Q9 TOWARD (reaffirmed); Q5 MIXED (top cond = PG's 2
clauses + implied 3rd + index-NLI below, toward; serial agg vs PG
parallel, away-leaning — parallel-admission gap). DS: Q4 TOWARD
strict (−qual; PG's top filter also carries customer_id — equality
placement differs); Q11 TOWARD (−qual; bushy secyear-first pairing);
Q84 MIXED (join-kind toward, top two levels text-identical modulo
Parallel; parallelism concretely away — R50's Gather Merge matched PG,
new shape serial); Q31 MIXED-leaning-toward (NL top = PG kind; +qual
9 redundant equalities; +scan = genuine CTE re-pairing, redundant
signal); Q47/Q57 NEUTRAL-TO-AWAY-cosmetic (+qual doubled conds; leg
order away from PG's v1_lead-outer; dominant WindowAgg gap unchanged);
Q25/Q64/Q72 tag-flat NEUTRAL-to-toward (transitive probe edges,
PG-verbatim probe on Q64); text-only = duplicate conds (Q58-class,
Q58 Limit 4.01..4.02→4.06..4.07), leg swap (Q83), ERROR-filename churn
(Q36/Q70/Q86). Findings for costing half: redundant-clause eval cost
visible (DP-minimisation = hypothesis, edge-admission unaudited);
synth-opened orders lose parallel paths. Next: join-order costing half
(`join_search_one_level` + parallel admission); nullable-side
assertion still open.

## R53 — join-order costing half, Step 0: measure Q9's divergence (IN PROGRESS 2026-09-10, report `r53-q9-costing-step0/REPORT.md`)

Question: with candidates open (R51), at which DP level does goopg's
pick first diverge from PG's Q9 order, and does the deciding term live
in sizing (`sizeJoinRel`) or pricing (`addPaths`)? PG: ((((ps⋈p)⋈s)⋈n)
⋈NL l)⋈o; goopg R51: ((((ps⋈p)⋈s)⋈NL l)⋈o)⋈n — shared through L3,
diverge at L4 ({ps,p,s}+n vs {ps,p,s}+l). Instrument: `DPTRACE cost`
line per relset (level, rows, pathlist length, cheapest kind + total)
so every future costing slice gets L-numbers without re-instrumenting.
Deliverables: Step-0 numbers + scoped pricing slice; review; commit;
push. Nullable-side assertion (R51 item 3) rides a later round.

Step-0 DONE 2026-09-10 (report written): PRICING at L6, hash arm, 2.5%
margin (winner 616861.02 vs nearest hash rival 632364.99); sizing OUT (L5
rows 303093=303093) and admission OUT (PG L6 partition offered phase 1,
zero lev-6 declines); reqouter={} on all L4–L6 winners (no
parameterisation dimension). Instrument carries reqouter + runner-up
(second/secondtotal) beyond the line above. Gates green: full
`internal/optimizer` + `testutil/estimateaudit` suites; Q12=2/Q13=34
canonical on clone-tpch (trace-off production config). Agent review
APPROVE-WITH-NOTES 2026-09-10, notes applied. Remaining: commit → push.
