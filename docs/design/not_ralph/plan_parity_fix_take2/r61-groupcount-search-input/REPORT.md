# R61 REPORT — group-count input rows: size the grouping rel from the search (2026-09-11)

SCOPE: `r61-groupcount-search-input/SCOPE.md` (+ §7 AMENDMENT A1). R60 LANDED
(`r60-partial-nestloop-producer/REPORT.md`): partial-NL producer live,
pp 5/15/0/2, DS PASS=94 + Q72 TIMEOUT.

## 1. Result: all five gates PASS, P1/P2 LAND, P3/P4 hold

The cut is two lines of sourcing + one shared helper across three files
(no estimator, Yao, selectivity, display-code, or executor change):

- `internal/optimizer/groupingpaths.go` (+22/−3): new `groupCountInputRows`
  helper (R54 `searchedJoinInputRelOf` gate verbatim, fail-closed to
  `EstimateRows`); `sizeGroupingRelFromAgg` consumes it.
- `internal/optimizer/cardinality.go` (+5/−1): `estimateAggregate` consumes
  the same helper (AMENDMENT A1 second consumer).
- `internal/optimizer/partialaggpaths.go` (+4/−2, comment only): C-15
  "blind Rows of 1 is harmless" superseded by consistency-is-the-justification.

Headline numbers (bin `goopg-r61`, clone `:5533`, seed 20260905):

| Query | Before | After | PG oracle |
|---|---|---|---|
| Q11 HashAggregate / Sort-above | rows=1 | **rows=26** (80 groups × 1/3) | 10667 (full #2+#3: 32000 × 1/3) |
| Q11 search `grouped.Rows` | 1 | **80** | 32000 (ledgered #2: Yao-through-probe) |
| Q5 HashAggregate / Sort-above | rows=1 | **rows=25** (n_name nd=25) | **25 — top-level rows now EXACT** |
| Q11 seed join rows | 32000 | 32000 (R54 gate unmoved) | 32000 |

The 26 = 80 × 1/3 chain confirms P2's HAVING-selectivity prediction
(`OpGt → rangeOpSelectivity → defaultIneqSel`) empirically — ledger #3
converts from verify-or-ledger to VERIFIED (mechanism: `Filter`-above-`Aggregate`
preserved, sel 1/3 exact: 80/3 = 26.67 → 26).

## 2. Gate ledger

1. `go test ./internal/optimizer/` green (2.364s, no `-count=1`); `go vet` clean.
2. **Values**: q1/q3/q5/q7/q8/q9/q10/q19 md5 MATCH vs `/tmp/pp2/r59/` (8/8);
   q11 values MATCH vs `goopg-r61base` (md5 `b8337742…`, 1304 rows both).
   Seed join rows stay 32000; Q5 groups pinned at 25.
3. **DPTRACE A/B** (`dp-r61base.txt` vs `dp-r61.txt`, 5534 lines each):
   **11 modified, 0 added, 0 removed** — every delta is an
   `upper.groupagg.hashed/sort` or `upper.ordered.sort` ROWS/total line
   (1→80/25/26 + per-group cost terms); **all join traces bit-identical**,
   all verdicts unchanged (accepted→accepted, dominated→dominated).
   Grouped-CTE-feeding-a-join cases A/B **IDENTICAL** on both binaries —
   including the strong case (CTE body = grouped lineitem⨝orders HASH JOIN
   feeding a customer join): the fail-closed gate holds, no shape shift.
   Residual (not blocking): the arm observably does not fire on CTE-body
   joins — likely CTE bodies never enter the `markSearchedTree`-tagged
   search, or the Finalize-agg→Gather→Partial-agg chain stops the walk at
   the inner Aggregate. Adjudicated as fail-closed-correct; mechanism to
   §6.
4. **pp**: `match=5 shapediff=15 unparsed=0 missingnode=2` — R60's 5/15/0/2
   exactly. Match set Q1/Q6/Q10/Q14/Q15a-VIEWBODY; missing = Q5/Q8
   (PG-only Materialize — queued triage (a), unchanged). Q5/Q11 number-moves
   are the predicted P1/P2 rows/costs; node-type shapes identical.
5. **DS SF0.5 sweep** (foreground, fresh rebuild — md5-identical to the
   A/B binary, provenance confirmed; private lane: fresh `cp -a` clone
   `/tmp/pp2/clone-ds05-r61` :5534 via tmp-only script copy, bench
   cluster never touched): `sweep-20260911-115225.txt` —
   **PASS=94 (57 ck-verified, 37 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0
   TIMEOUT=1 (Q72 alone, 325s vs R60 318s / R59 320s) SKIP=4**
   (Q4 oracle-TIMEOUT + 36/70/86 querygen). Status-delta vs R60:
   **verdict-changes=none, runtime-moves=1** (Q20 3s→6s), total −1.1%.

## 3. DS plan-shape channel: 28 changed adjudicated (non-blocking, P4 holds)

Plan-diff vs R60: same=71, changed=28. Mechanical classification
(number-stripped plan equality): **21 pure number-moves** (agg rows 1→N /
292→293-class propagation + cost terms — the predicted blast radius;
e.g. DS-Q3 HashAggregate 1→15, the same disease as TPC-H Q11) and
**7 cost-driven shape moves**, all values-PASS (3 ck-verified), none a
runtime-mover:

| Q | Move | vs PG oracle (`bench/tpcds/plans-pg`) |
|---|---|---|
| Q39 | NL→HJ ×2 | PG uses HJ chains — TOWARD |
| Q40 | Sort+HashAgg → GroupAgg+Sort | PG uses Finalize GroupAggregate — TOWARD (strategy; PG additionally parallel) |
| Q47/Q57 | inner MJ→HJ (+side swap) | PG nests HJ chains at this level (outer 1-row MJ unchanged) — TOWARD |
| Q64 | NL/HJ-family reorder + cond-side swap | PG is itself a deep NL chain; family preserved, no timing delta — benign |
| Q74 | CTE-scan order swap | commutativity, same family — benign |
| Q79 | top → Gather Merge | PG parallelises this query too (Gather Merge inside); top strategy differs (GM vs MJ), no timing delta — toward in parallelism dimension |

Mechanism for all 28: corrected group counts reprice agg-feeding
subtrees, flipping strategy comparisons the old rows=1 priced wrong.
No new TIMEOUT, no CKMISMATCH — the planner working as designed.

## 4. Deviations from SCOPE (ledgered, none blocking)

1. **Lateral**: HAVING sel 1/3 VERIFIED empirically (was verify-or-ledger #3).
2. **Lateral**: DS private-lane reroute (fresh clone + tmp script copy with
   `SF05_GOOPG_DATA`/`SF05_LOG`/`SCRIPT_DIR` repointed; tools in-tree).
   Two self-inflicted probe faults found and fixed: `REPO_ROOT`/`SCRIPT_DIR`
   pointed at /tmp (server never started — immediate), then `CKSUM` resolved
   to /tmp so every query read grows=0 (caught on the 2-query probe because
   result files held 100 rows). Full sweep ran only after the probe went
   2/2 PASS.
3. **Residual → §6**: why `estimateAggregate` never fires on CTE-body joins.

## 5. Re-audit accounting

P0 fired (sr=32000 both aggs) but display stayed 1 → STOPPED per rule,
R61A probe isolated the no-`PlanCost` recompute consumer, AMENDMENT A1
reviewed into SCOPE (commit `d90730b0b`) BEFORE the delivery run. No
P-miss stands: P1 (search 80 + display 80×sel), P2 (26 via sel=1/3),
P3 (values all MATCH), P4 (94 + Q72 alone, verdicts unchanged) all LAND.

## 6. Ledgered follow-ups (unchanged + 2 additions)

- #2 Yao-through-parameterized-probe (80→32000), #3 CLOSED by §1 (sel
  verified 1/3), M1-display, `estimateNLIndexJoin` staleness comment,
  (a) Materialize producer, (b) Q4 agg/sort strategy.
- **NEW #4**: CTE-body join search tagging — determine why the R61 arm
  never fires there (walk-stop vs never-tagged) and whether CTE-body
  group counts should source from their own search.
- **NEW #5**: DS Q39/Q40/Q47/Q57/Q79 shape moves are toward-oracle but
  land the planner on shapes the executor runs without PG's Memoize/Gather
  refinements (cf. R59 Q72) — watch sweep-tail timing if group counts
  grow further under #2.

Evidence tmp-only `/tmp/pp2/r61/` (run.sh, tops/values/pp, dp A/B,
cte-g/cte-gj A/B, ds05/ incl. `sweep-20260911-115225.txt`, patched
`sweep-r61.sh`); clone `/tmp/pp2/clone-ds05-r61` :5534; binaries
`/tmp/pp2/bin/goopg-r61` (`88193692…`, rebuilt md5-identical) +
`goopg-r61base`.
