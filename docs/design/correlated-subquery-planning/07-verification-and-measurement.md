# 07 — Verification and Measurement

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| depends on | [01](01-current-state-and-gap-analysis.md) (scoreboard), [03](03-planner-decorrelation-extensions.md)–[06](06-cost-model-touchpoints.md) (designs being verified) |

This chapter defines how every phase of the roadmap ([08](08-roadmap-and-milestones.md))
proves itself: the semantics test matrix, oracle-parity protocol, plan gates,
performance gates, regression protocol, and the instrumentation that replaces
Fermi estimates with counters. It is written to be executable as acceptance
criteria — each phase in chapter 08 cites gates from this chapter by ID (V1…V6).

Guiding rule: **a decorrelation or SubPlan change is not "done" when the plan
looks right; it is done when the semantics matrix is green against the PG
oracle, the plan gate pins the new shape, and the perf gate confirms the win
on SF1.** Silent row-count regressions are this project's most expensive
failure mode; every gate below exists to make that class of failure loud.

---

## V1. Semantics test matrix

A table-driven unit/integration suite (new file, suggested location
`internal/testport/subquery_semantics_test.go`, plus planner-level unit tests
in `internal/planner`) that runs each case through goopg and asserts exact
results. Every case must ALSO be run through `scripts/pg-oracle-diff.sh`
(V2) so the expected outputs are pinned to PG 18.3, not to our reading of the
manual. User-visible semantics are specified in the official docs:
[`create_pg_super_document/official_doc_in_md/functions-subquery.md`](../../../create_pg_super_document/official_doc_in_md/functions-subquery.md)
(EXISTS / IN / NOT IN / ANY / ALL / scalar subquery expressions).

The matrix must stay green **whether or not decorrelation fires** — each row
is executed twice in CI-parity runs where feasible: once with the unnest pass
enabled (default) and once with it structurally suppressed (test hook or the
narrowest available knob), because the count-bug and NULL rows are exactly the
cases where the transformed and untransformed plans can silently diverge.

| # | Case family | Representative probes | PG-mandated result the row pins |
|---|---|---|---|
| M1 | `IN` NULL propagation | inner has NULLs; outer operand NULL; both; empty inner | `x IN (…)` yields NULL (not false) when no match but inner contains NULL; empty inner → false |
| M2 | `NOT IN` NULL propagation | same four sub-cases; **plus the correlated NULL-operand × empty-inner cross-case: `b NOT IN (SELECT b FROM t2 WHERE t2.a=t1.a)` where the operand is NULL and the correlated inner is empty** | any NULL in inner ⇒ `NOT IN` can never be true (NULL for non-matching rows) — the `NullAware` anti-join contract (M0122-0011); `NULL NOT IN (∅)` is TRUE (vacuous). **Live bug at HEAD** [measured-at-HEAD e4a43ba6]: the correlated SubPlan path returns `{2}` vs PG `{2,4}` on the review fixture ([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §4) — this row pins it; executor fix lands with S2/S3 |
| M3 | `EXISTS` / `NOT EXISTS` with NULL correlation values | outer correlation column NULL; inner rows NULL on the joined column | EXISTS ignores NULLs in the usual equality way — `NULL = x` never matches, so EXISTS is false for a NULL correlation value; NOT EXISTS true |
| M4 | Scalar subquery cardinality | zero inner rows; exactly one; more than one | zero rows → NULL (no error); &gt;1 row → error 21000 `more than one row returned by a subquery used as an expression` |
| M5 | Count-bug probes | `WHERE t.x > (SELECT count(col) FROM s WHERE s.k = t.k)` with outer rows having **no** match in `s` — **`count(col)`, not only `count(*)`: the `Star` gate bails `count(*)` into the (correct) SubPlan path, so a `count(*)`-only probe goes green while missing the live bug**; plus a COALESCE-wrapped aggregate (`COALESCE(sum(col),0)`); plus `count(*)`, `sum`/`avg`/`min`/`max`; plus a **multi-column correlation** variant (two-key GROUP BY, Q20's `(l_partkey, l_suppkey)` shape) | unmatched outer rows compare against `count = 0` (count is 0 on empty input, NOT NULL) — a naive INNER-join decorrelation drops them; `sum`/`min`/`max`/`avg` on empty input are NULL, so the comparison is NULL → row filtered. **Live bug at HEAD** [measured-at-HEAD e4a43ba6]: `count(b)` returns `{3}` vs PG `{2,3,4}` ([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §2). Guards D3.4; whitelist is S1-blocking |
| M6 | OR-position sublinks | `WHERE a = 1 OR EXISTS(…)`, `WHERE a = 1 OR x IN (SELECT …)`, `WHERE NOT (x IN (SELECT …))`, `WHERE a = 1 OR y > (SELECT agg …)` | must NOT decorrelate (PG keeps SubPlans here — `pull_up_sublinks_qual_recurse` stops at non-AND nodes, `postgres/src/backend/optimizer/prep/prepjointree.c`); results must still be correct via the SubPlan path. **⚠ Live bugs at HEAD** [measured-at-HEAD e4a43ba6] ([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §§1,3): the IN-under-OR and `NOT (x IN …)` probes **hang the planner** (infinite loop — run them only under `timeout` until the ch.03 §2.5 guard lands in S1), and the OR-position scalar probe returns wrong results (`{}` vs PG `{2}`) |
| M7 | Sublinks in outer-join ON clauses | `LEFT JOIN … ON EXISTS(…)`, sublink referencing the nullable side | correctness audit owned by ch.03 §8.5 — audit executed in S0, any needed guard lands in S1; PG restricts pull-up here (prepjointree.c header comment) — goopg must not produce a plan that changes LEFT JOIN row preservation |
| M8 | Multi-level correlation (`Level` &gt; 1) | subquery inside subquery referencing the outermost query's column | value threading through two `OuterColumnRef` levels (`internal/planner/plan.go` `OuterColumnRef.Level`); both SubPlan eval and any future param-slot implementation (D4.1) must agree |
| M9 | Correlated `IN` operand safety | `expr(outer) IN (SELECT …)` where the operand itself is NULL or volatile-free composite | pins `correlatedInOperandSafeToUnnest` behavior (`internal/planner/unnest.go`) |
| M10 | `= ANY` / `<> ALL` forms | operator-ANY/ALL variants of M1/M2 | ANY/ALL forms follow the same NULL algebra; goopg maps them onto `InExpr` (`AnyOp`/`AllOp` fields) |
| M11 | Non-correlated sublink caching | repeated executions in one query; sublink under prepared statement re-execution | one evaluation per query (constant cache key, M0058-0001); results identical across outer rows; cache must not leak across queries |
| M12 | EXISTS with inner-only residual + LIMIT/DISTINCT/aggregate bodies | `EXISTS(SELECT … LIMIT 1)`, `EXISTS(SELECT DISTINCT …)`, `EXISTS(SELECT count(*) …)` | EXISTS over an aggregate body is **always true** (aggregate returns a row); LIMIT/DISTINCT are no-ops for EXISTS truth value — these gate which bodies D3.0/S1 may unnest |
| M13 | Volatile / side-effecting subqueries | `EXISTS(SELECT … WHERE random() < 0.5)`; a scalar subquery calling a volatile function; `EXISTS(SELECT … FOR UPDATE)` (LockRows inside a sublink) | such subplans must re-execute per outer row: PG marks every subplan param changed unconditionally (`postgres/src/backend/executor/nodeSubplan.c:236-244`) and Memoize refuses volatile inners (`postgres/src/backend/optimizer/path/joinpath.c:770-800`). goopg must disable result caching AND any rescan-skip when the inner plan contains a volatile function or a LockRows node — the D4.2/D4.4 cacheability gate, delivered in S2. `FOR UPDATE` inside EXISTS must stamp row locks for every qualifying outer row, never serve them from a cache |
| M14 | Non-equi-only correlation | `EXISTS(SELECT 1 FROM s WHERE s.v > t.x)` (range-only correlation, zero equijoin pairs); same for `IN` operand shapes | semantics must be identical whether served by the SubPlan path (today) or by a future NL semi/anti join (D3.2/S4, D6.2/S6) — both paths exercised once the NL variant exists; guards D3.2's "pure non-equi → NL or SubPlan" rule |
| M15 | Nested sublink inside a pulled-up EXISTS, one sublink deep | the full D3.3 shape (`EXISTS(… AND x IN (SELECT …))`) itself wrapped one sublink deep, with the innermost sublink referencing the **outermost** query (`Level 2` through the pulled-up body) | after D3.3's pull-up, the retained inner SubPlan's outer references must resolve to the same scopes as before — the F7 wrong-scope hazard (`OuterRows[len-2]` silently hitting the grandparent) must produce correct results, not a silent scope shift; guards D3.3's deep-walk precondition |
| M16 | Scalar residual-lifting × aggregate safety | correlated scalar with an extra non-equi outer conjunct (`WHERE s.k = t.k AND s.d < t.d`) under each whitelisted aggregate | a lifted residual filters join rows **before** aggregation is re-established — legal only for NULL-on-empty aggregates (ch.03 §5 constraint); the row pins that D3.2's lifting never changes the aggregate's input set semantics vs the SubPlan path; guards D3.2 × D3.4 |

Each "target: unnest" cell in chapter 03's coverage matrix MUST have at least
one row here exercising both the transformed and untransformed path (the
bundle's adversarial review must check this mapping).

Upstream regress coverage: the canonical upstream suite for this area is
`postgres/src/test/regress/sql/subselect.sql`. It is not individually tracked
in `docs/test-port/postgres-oracle-port-status.csv` — the whole regress suite
is entry **D-001** (`status=defer`, pending normalization policy) — but the
runner already exists. **Work item (S0):** run
`scripts/pg-regress-runner.sh -v subselect`, record the current parity
percentage in the chapter-01 dossier, and re-run it as part of every phase's
regression protocol (V5). Rising subselect parity is the external yardstick
for this bundle; promoting `subselect` into the runner's default quick set
should happen no later than S1.

## V2. Oracle parity (pg-oracle-diff)

Protocol for every semantics row and every rewrite added by chapters 03–05:

```bash
# One-shot inline probes (both servers running):
scripts/pg-oracle-diff.sh --sql "SELECT … EXISTS …"

# Matrix files (checked in next to the test):
scripts/pg-oracle-diff.sh --auto-start internal/testport/testdata/subquery_semantics.sql
```

- A `PASS` means goopg output matches PG 18.3 after normalization; any `FAIL`
  is a goopg bug — never a reason to adjust expectations.
- TPC-H answer-set parity: after each phase, the 22-query stream on SF1 (and a
  quick SF0.1 load where iteration speed matters) must return row counts and
  row contents matching PG on the same data. The plan-compare methodology
  (identical GUCs, ANALYZE'd, identical query text from `internal/testutil/tpch`)
  is the template: `analysis/tpch/goopg-pg-tpch-plan-compare-260718/` (on
  `origin/master`, commit `be4f0291`; not on branch `wal-pg-nodetree`).
  Caveat: HammerDB loads are per-engine generated, so `lineitem` differs ~0.007%
  between independently loaded clusters — row-**count** parity is asserted on
  goopg's own load against pinned counts, and content parity is asserted on the
  shared-load harness used by `scripts/pg-oracle-diff.sh`.

## V3. Plan gates (EXPLAIN shape assertions)

Two mechanisms, both already in-repo, extended for this bundle:

1. **`make plan-gate`** — captures live EXPLAIN output for the TPC-H set via
   `cmd/plan-snapshot` and diffs it (default `MODE=structural`) against the
   newest baseline in `plan_snapshots/`. SKIPs cleanly when no baseline or no
   bench server exists, so it never hard-blocks. Phase protocol: capture a
   labelled baseline **before** starting a phase
   (`make plan-snapshot-capture LABEL=csq-sN-pre`), and one after landing it;
   the pre/post diff is attached to the phase's closing commit. From S1 on, a
   baseline capture is mandatory in the same loop as any `internal/planner`
   change (this is the existing executor/planner practice-card rule).
2. **Per-query shape assertions** (new, small Go tests over `EXPLAIN` text
   against the bench server, colocated with the existing spot-check tooling):
   pinned expectations, updated deliberately per phase:

| Query | Asserted shape (end state; phase where it flips) | Tag |
|---|---|---|
| Q4 | `Semi Join` (hash or NLI) on `l_orderkey = o_orderkey`; no `<*planner.ExistsExpr>` in plan text (S1) | [measured-at-HEAD e4a43ba6]: currently `Seq Scan orders + Filter(ExistsExpr)` |
| Q21 | Semi Join + Anti Join on `l_orderkey`, residual `<>` predicates on the join (S1, refined S4/S6) | currently both EXISTS as MHJ leaf filters |
| Q22 | Anti Join on `o_custkey = c_custkey`; non-correlated scalar avg stays a cached SubPlan or InitPlan-equivalent (S1) | currently `NOT ExistsExpr` filter on Seq Scan |
| Q2 / Q17 / Q20 | correlated scalar either decorrelated (GROUP BY + join) or executed as rescan-SubPlan with cache counters showing O(distinct keys) executions, not O(outer rows) (S1/S2) | currently `SubqueryExpr` filters |
| Q16 | non-correlated `NOT IN` subquery stays unnested (`Hash Join` vs supplier, already fires at HEAD — chapter 01 §4); the residual `<*planner.InExpr>` literal `p_size IN` list is exempt from the opaque-string assertion until chapter 06 §6's rendering lands (S0) | [measured-at-HEAD e4a43ba6]: subquery unnested; literal IN-list filter remains |
| ALL 22 | **end-state assertion (post-S4):** no `<*planner.` opaque expression string appears in any TPC-H EXPLAIN output — every sublink is either decorrelated or printed as a first-class SubPlan node (chapter 06 §6 EXPLAIN visibility, lands in S0) | grep-level check over the capture file |
| Q12 / Q13 | **canonical tripwires**: row counts 2 / pinned Q13 value (`bench/tpch/spotcheck_expected.env`) via `scripts/tpch-spotcheck.sh` on every commit of every phase; plan shape for Q12/Q13 must be byte-stable through S5's pipeline reorder (they contain no sublinks — any drift is collateral damage and blocks the phase) | existing gate |

Evidence baseline for all "currently" cells:
[`evidence/explain-head-e4a43ba6.txt`](evidence/explain-head-e4a43ba6.txt) and
[`evidence/unnest-probes-e4a43ba6.txt`](evidence/unnest-probes-e4a43ba6.txt).

## V4. Performance gates

Scoreboard discipline: targets are set against the **measured HEAD baseline**,
not the May-2026 record. [measured-at-HEAD e4a43ba6] (SF1, bench data dir,
single warm run): **Q22 = 1.80 s (≈31× PG's 0.058 s), Q4 = 7.41 s (≈39× PG's
0.188 s)**. The historical 1156×/1452× ratios in the plan-compare §7 were
measured on a 2026-05-26 build and are cited as history only. Q2/Q7/Q8/Q17/
Q20/Q21 SF1 runtimes at HEAD are **unmeasured**; filling them is an S0
deliverable, and their per-phase targets below are provisional until then.

Method: same harness as the HEAD capture (capped server via
`scripts/goopg-test-run.sh`, bench data dir, `cmd/tpch-runner`, warm cache,
report best-of-3). PG reference numbers are the plan-compare §4 warm single
samples on the same host.

| Gate | When | Target | Rationale |
|---|---|---|---|
| P-S1a | after S1 | Q4 ≤ 3 s | hash/NLI semi join replaces the per-row EXISTS SubPlan invocations — calls ∈ {≈57 K if the date-range conjunct short-circuits first, ≈1.5 M if the EXISTS is evaluated for every `orders` row}; both figures are estimates derived from the plan-compare PG actuals, decided by the S0 counters (V6). The probe-side work after the rewrite is a single `lineitem` pass or the equivalent index probes (~1.5 s Fermi from `analysis/tpch-runner-measurement-report-2026-05-06.md` §2.3) |
| P-S1b | after S1 | Q22 ≤ 1 s; Q21 measurably improved vs its S0 baseline | anti join on `orders`; Q21 partially unlocked (its residual `<>` handling may wait for S4) |
| P-S2 | after S2 | Q4 ≤ 1 s; no TPC-H query &gt; 2× its S1 time (no-regression clause) | rescan-not-rebuild removes per-invocation Build/Open/Close from every remaining SubPlan site; win shows wherever SubPlans survive (Q2/Q17/Q20 class) |
| P-S3 | after S3 | Q16/Q18-class stable or improved; M2 matrix green with hashed NOT-IN | hashed SubPlan replaces linear `collectInValues` probes |
| P-S4 | after S4 | Q20 and Q21 within 5× of PG; count-bug matrix (M5) green | coverage extensions land the remaining decorrelations |
| P-S5 | after S5 | geomean over Q2,Q4,Q7,Q8,Q17,Q20,Q21,Q22 within the bulk-operator band (≈19–40× of PG, per chapter 01 §measured) with **no individual query &gt; 60×**; Q12/Q13 plans byte-stable | after the reorder, semi/anti placement is join-order-optimized; residual constant-factor gap (~19× single-thread Go vs parallel C) is explicitly **out of scope** — owned by the perf-optimize line |
| P-S6/S7 | after S6/S7 | NLI-semi/anti chosen where PG chooses index anti-join (Q21 shape); Memoize opt-in shows ≥10× on a synthetic high-duplication NLI microbench, no TPC-H regression | index probes with first-match early-out are PG's winning Q21 shape; Memoize's value is duplication-dependent and absent from TPC-H SF1 (ch.05 §6), hence a synthetic microbench plus a no-regression clause instead of a TPC-H target |

A perf gate failure does not roll the phase back by itself; it triggers the
instrumentation protocol (V6) and a written explain-delta before the phase can
close.

## V5. Regression protocol (every phase)

Ordered; all FOREGROUND (loop sessions kill background tasks at turn end):

1. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — unit and
   component suite (mandatory before every commit; the `.githooks/pre-commit`
   hook then runs the pgbench smoke on the commit itself — never bypass with
   `--no-verify`).
2. `scripts/tpch-spotcheck.sh` — Q12/Q13 canonical row counts from a fresh
   server (mandatory for every executor/planner/codec change).
3. `make plan-gate` — structural EXPLAIN diff vs the phase's `csq-sN-pre`
   baseline (see V3).
4. Semantics matrix (V1) + `scripts/pg-oracle-diff.sh` runs (V2), including
   `scripts/pg-regress-runner.sh -v subselect` parity (must be
   non-decreasing).
5. Full TPC-H SF1 22-query timed stream; update the chapter-01 scoreboard
   table in the same commit.
6. `make race-gate` — mandatory for phases touching executor caches or
   introducing shared state (S2, S3, S7): the SubPlan caches today live on the
   per-query `Context` and rely on the single-goroutine-per-query execution
   model; any deviation (e.g. a future shared Memoize arena) must survive the
   race detector.
7. End-of-bundle (post-S5): regenerate the plan-compare artifact on the
   current branch (same methodology as
   `analysis/tpch/goopg-pg-tpch-plan-compare-260718/`), fixing the
   stale-artifact problem documented in chapter 01 — the July-2026 artifact
   captured plans from a branch whose runtime behavior had already diverged
   from its own §7 timing table.

## V6. Instrumentation (lands in S0, used by every later phase)

Per-SubPlan-site counters, surfaced through `EXPLAIN ANALYZE` (and readable in
tests), so gap analyses stop relying on Fermi arithmetic:

- `calls` — number of evaluations of this sublink (per query execution).
- `rebuilds` — how many of those performed a full operator-tree
  Build/Open/…/Close cycle (today: == cache misses; after D4.2: only true
  rebuilds).
- `rescans` — evaluations served by re-opening/re-positioning an existing
  operator tree (D4.2 path; the PG analog is `ExecReScan` after `chgParam`,
  `postgres/src/backend/executor/nodeSubplan.c`).
- `cache_hits` / `cache_misses` — `SubqueryCache` / hashed-SubPlan / Memoize
  hits and misses, per site.
- `peak_cached_bytes` — high-water memory of the site's cache (feeds D4.5 /
  D6.4 budget decisions).

Display: attached to the owning node in `EXPLAIN ANALYZE` output as
`SubPlan (calls=N rescans=N rebuilds=N hits=N misses=N)`; exact format is
fixed in chapter 06 §6 together with the first-class SubPlan node printing.
The counters' S0 role is to confirm **magnitudes** (e.g. Q4's call count,
per-query gate attribution) — the non-firing *mechanism* itself is already
known (`IndexScan.Key` absorption, ch.01 §5 / ch.03 §2.1).
Implementation note: counters hang off the existing per-site cache structures
on `internal/executor/context.go` (`SubqueryCache`, `CorrSubqOps`,
`CorrSubqHashMaps`), so S0 can land them without any planner change.

Acceptance for S0 instrumentation: running Q4 at HEAD shows
`calls == rebuilds` and `rescans = 0` for the EXISTS site, at a magnitude
consistent with the executor's conjunct ordering — ≈1.5 M if the EXISTS is
evaluated for every `orders` row, ≈57 K (the ~3.7 % date-filtered subset,
estimated from the plan-compare PG actuals) if the date-range conjuncts
short-circuit first. The counters must reproduce the known pathology
(every call a full rebuild, zero rescans) before we trust them to certify
its fix; whichever magnitude they report becomes the pinned baseline.

---

## Gate ↔ phase summary

| Phase | Blocking gates |
|---|---|
| S0 | V6 counters land + reproduce Q4 pathology; V1 matrix implemented — green at HEAD behavior **except the known-bad rows, which are pinned as expected-fail entries (not skipped): M2's correlated NULL-operand case, M5's `count(col)` case, and M6's hang-prone probes (run under `timeout`, expected-fail until the S1 guard)**; subselect parity recorded; scoreboard filled (V4 baselines) |
| S1 | V1 (esp. M3/M12, plus flipping M5/M6's expected-fail rows to green via the ch.03 §2.5 guards + D3.4 whitelist), V2, V3 shapes Q4/Q21/Q22, P-S1a/b, V5 |
| S2 | V1 M8/M11/M13, P-S2, race-gate, V5 |
| S3 | V1 M1/M2/M10, P-S3, race-gate, V5 |
| S4 | V1 M5/M6/M7/M9/M14/M15/M16, V3 Q20/Q21 shapes, P-S4, V5 |
| S5 | Q12/Q13 plan byte-stability + full V3 sweep, P-S5, V5 incl. plan-compare regeneration |
| S6/S7 | V3 NLI-semi/anti shapes, P-S6/S7, race-gate (S7), V5 |
