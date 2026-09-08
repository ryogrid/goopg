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
- [ ] **R20 — partial aggregation over the flip's Gather** (K23). The
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
- [ ] **R21 — slice (B): let a node below satisfy the ordering** (K12
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
- [ ] **R21 — index-leaf repricing hole** (§7.2, `joinsearch.go:480`),
  now with K6's evidence: the winning scans in these plans are PREBUILT
  leaves priced by `costSeqscan` with `numQualOps = 0`, so R1's charge
  never reached them. Fixing this is the precondition for testing
  DESIGN §5's suspect #1.
  Give index leaves their qual charge instead of `numQualOps = 0`.
- [ ] **R22 — unconditional plain-index-scan arm** (§7.3). Drop/relax the
  `hasUsefulPathkeys` gate so a plain index path is always a candidate.
- [ ] **R23 — persist correlation** (§7.4). Connection-scoped ANALYZE
  loses correlation across restart → `corr = 0` → every index scan at
  `max_IO_cost` (`costindex.go:407-420`).
- [ ] **R24 — re-measure the ONEREL flip.** E-21 Cut 1b routes
  single-table statements through the search behind `GOOPG_ONEREL_SEARCH`
  (default OFF, deliberately — removing the rule chooser made plans
  worse under the §3 asymmetry). After R1/R2 change the prices, re-run
  the flip A/B (values + parallel-mode Gather capture + timing). The
  diversion may become closable.

## Log

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
