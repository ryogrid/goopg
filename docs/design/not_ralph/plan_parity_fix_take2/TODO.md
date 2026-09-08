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
  Hypotheses (consistent, unconfirmed): goopg's `numeric` is bigger
  than PG's and its `character(N)` is not blank-padded.
  **`relpages` is a planner INPUT** — every page-priced term, the
  Mackert-Lohman estimate, and the parallel-worker thresholds — so a
  15% page error separates otherwise-identical plans and **no planner
  change can close it**. Part of this goal is therefore not planner
  work; it is on-disk representation, and as such a PG-compat defect in
  its own right. Detail: `r4-heap-density/FINDINGS.md`.
- **K4 (rev-1 error pattern, from §6).** Never conclude from a file
  without checking its callers (`pathgen.go`/`generateScanPaths` is
  test-only; production seed is `newPrebuiltPath`). Every design must
  cite call sites, not files.

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
- [ ] **R5 — confirm the per-type storage sizes** (K14 follow-up).
  Measure single-column tables (`integer`, `numeric(7,2)`,
  `character(10)`, `varchar`) against PG and locate the divergence in
  goopg's tuple encoder. Converts K14's hypotheses into facts, cheaply.
  NOTE: this is an on-disk-format question, not a planner one.
- [ ] **R6 — index-leaf repricing hole** (§7.2, `joinsearch.go:480`),
  now with K6's evidence: the winning scans in these plans are PREBUILT
  leaves priced by `costSeqscan` with `numQualOps = 0`, so R1's charge
  never reached them. Fixing this is the precondition for testing
  DESIGN §5's suspect #1.
  Give index leaves their qual charge instead of `numQualOps = 0`.
- [ ] **R7 — unconditional plain-index-scan arm** (§7.3). Drop/relax the
  `hasUsefulPathkeys` gate so a plain index path is always a candidate.
- [ ] **R8 — persist correlation** (§7.4). Connection-scoped ANALYZE
  loses correlation across restart → `corr = 0` → every index scan at
  `max_IO_cost` (`costindex.go:407-420`).
- [ ] **R9 — re-measure the ONEREL flip.** E-21 Cut 1b routes
  single-table statements through the search behind `GOOPG_ONEREL_SEARCH`
  (default OFF, deliberately — removing the rule chooser made plans
  worse under the §3 asymmetry). After R1/R2 change the prices, re-run
  the flip A/B (values + parallel-mode Gather capture + timing). The
  diversion may become closable.

## Log

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
