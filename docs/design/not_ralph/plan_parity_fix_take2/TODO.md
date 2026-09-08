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
- **Parity criterion**: `scripts/pg-plan-parity-diff.py` (TPC-H) +
  `scripts/tpcds-plan-diff.py` (TPC-DS) classifications
  (match / shapediff / …). Report per-round movement on BOTH corpora.
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
- [ ] **R1 — qpqual on the index path** (root-causes §7.1). Charge PG's
  `(cpu_tuple_cost + qpqual) × tuples_fetched` (+ startup) for
  non-index-satisfied quals at all five index-cost sites. Design must
  define WHERE the qual list comes from per site (no new candidates).
  Gates: optimizer/executor suites, values both suites, parity A/B both
  corpora (expect Q12-class moves toward PG), timing table reported
  (not adjudicated unless shapes move unexpectedly).
- [ ] **R2 — index-leaf repricing hole** (§7.2, `joinsearch.go:480`).
  Give index leaves their qual charge instead of `numQualOps = 0`.
- [ ] **R3 — unconditional plain-index-scan arm** (§7.3). Drop/relax the
  `hasUsefulPathkeys` gate so a plain index path is always a candidate.
- [ ] **R4 — persist correlation** (§7.4). Connection-scoped ANALYZE
  loses correlation across restart → `corr = 0` → every index scan at
  `max_IO_cost` (`costindex.go:407-420`).
- [ ] **R5 — re-measure the ONEREL flip.** E-21 Cut 1b routes
  single-table statements through the search behind `GOOPG_ONEREL_SEARCH`
  (default OFF, deliberately — removing the rule chooser made plans
  worse under the §3 asymmetry). After R1/R2 change the prices, re-run
  the flip A/B (values + parallel-mode Gather capture + timing). The
  diversion may become closable.

## Log

- 2026-09-08 R0 done: baseline captured (TPC-H 1/12/8 over 21; TPC-DS
  0/96 real matches; 36/70/86 unplannable on both). Subagent delegation
  unavailable in this environment (Task call cancelled) — R0 executed
  directly, all foreground. Servers :5543/:5544 left running.
- 2026-09-08 R0 started: K1/K2 verified against tree + oracle; tooling
  confirmed. Branch `plan-parity-with-pg-take2`.
