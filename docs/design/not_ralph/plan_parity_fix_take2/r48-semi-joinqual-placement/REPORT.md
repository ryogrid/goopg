# R48 — implementation report (both halves LANDED 2026-09-10)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Design: `DESIGN.md` (APPROVE-WITH-NOTES, notes applied). Step-0 pair:
`pg-q4.txt` + `goopg-q4-s2.txt` (this dir).*

## 0. What landed

**Half 1 — `Filter: (true)` drop (EXPLAIN-only).**
`internal/executor/operators_explain.go`: both Filter arms
(`walkPlanFiltered` plain + `walkPlanAnalyzeFiltered` ANALYZE twin)
pass through a `*Filter` whose predicate is `*BooleanConst{Value:true}`,
carrying the incoming attached filter unchanged. Nil predicates and
`Value:false` take the normal path. Pins:
`internal/executor/explain_truefilter_skip_test.go` (5 tests, each
through BOTH walkers per the file's sibling-agreement doctrine).

Scope extension beyond DESIGN §1, recorded honestly: the design named
only `walkPlanFiltered`; the ANALYZE walker carries a twin Filter arm
and the file's own doctrine says a test pinning one proves nothing
about the other — so the skip went into both. A true predicate
rejects 0 rows, so the ANALYZE `fr` accumulation before pass-through
loses no "Rows Removed by Filter" count.

**Half 2 — semi JoinQual placement (planner).**
`internal/optimizer/nl_index_join.go`: new `lowerSemiResidualToCond`
helper + call in `tryBuildNLI` after the rebind block, before
`committed = true` (i.e. BEFORE `indexOnlyNLIInner` — the F2 order
pin: the IOS check sees `Cond` set and declines; the reverse order
would promote and drop the qual silently). SEMI/ANTI-gated at the
call site; delegates side tests to `classifyConjunctSide`
(inner-only = `sideRight`; mixed/out-of-scope/ref-less stay);
movers cloned via `cloneExprShiftIdx(c, -outerWidth)` (veto → keep);
whole move declined when the probe already carries a `Cond`.
For SEMI/ANTI `pickInnerSide` only admits `j.Right` as inner, so the
outer++inner rebind frame is exactly the Left++Right frame — no frame
hazard (that hazard exists only for INNER's swappable sides, gated
out). LEFT never routes here (null-extended-row safety); INNER
deferred per design.

Pins: `internal/optimizer/nl_index_join_cond_lower_test.go` (7 tests:
semi+anti inner-only moves; outer-referencing stays; mixed splits +
stays `*IndexScan` — the F2 order pin; LEFT keeps; `OuterColumnRef`
stays while the inner-only half beside it moves; Q4-shape
Filter{SeqScan} unwrap → Cond; helper decline paths) with a
`assertCondLeafLocal` Cond-coordinate assertion (name AND range
against the probe output). Plus placement pins at the other two
levels: the S6 planner end-to-end
(`TestIndexKeyExistsResidualBecomesNLISemi`, updated from
"residual on the NLI" to "residual on the probe Cond, Predicate
nil") and the executor rows+EXPLAIN pins
(`TestNLISemiResidualExecution` / `TestNLIAntiResidualExecution`:
exactly one `Filter: (l_c < l_r)` line — the probe's — with rows
{1,3}/{2,4} incl. NULL-residual semantics, and
`TestNLISemiResidualMatchesSubPlanPath` NLI↔SubPlan agreement).

## 1. Census + adjudication (DESIGN §3.1–3.2)

Corpora: `/tmp/pp2/oc-tpch-r48h1.txt` (post-Half-1) →
`/tmp/pp2/oc-tpch-r48h2.txt`; `/tmp/pp2/oc-ds05-r48h1.txt` →
`/tmp/pp2/oc-ds05-r48h2.txt` (clones `:5533`/`:5534`, binary
`/tmp/pp2/goopg-r48h2`, inode-verified launch, same pinned GUCs).

- **TPC-H: 9 changed lines, all adjudicated moves.** Q4: join-level
  `Filter: (l_commitdate < l_receiptdate)` gone, inner Index Scan
  gains `Filter: (l_commitdate < l_receiptdate)` — PG placement.
  Q21: outer Anti's mixed residual
  `(l_suppkey <> l1.l_suppkey) AND (l_receiptdate > l_commitdate)`
  splits — outer-only `l_suppkey <> l1.l_suppkey` stays on the join,
  inner-only `l_receiptdate > l_commitdate` moves to the `l3`
  probe (the F2 mixed case live on real TPC-H). Nothing else moves.
- **TPC-DS SF05: byte-identical** (PID-noise masked) — Half-2 is a
  strict no-op there; no semi/anti NLI with inner-only residuals
  in the corpus.
- Half-1 (prior segment, restated for the record): stray census
  6 → 0 TPC-H, 34 → 0 TPC-DS; normalized shape diffs empty on
  both corpora (ZERO EXTRA flips). Fossil-carrier finding: the
  true-wrapper is a fully-costed Filter whose predicate was
  swapped post-costing while stored PlanCost stayed — Q2 host
  rows 5 → 160000, adjudicated PG-EXACT against the live oracle
  (`Nested Loop (cost=9.37..21684.53 rows=160000 width=177)`).

## 2. Q4 line-by-line vs the archived Step-0 pair (DESIGN §3.5)

Post-R48 goopg Q4 (`oc-tpch-r48h2.txt`):
`Nested Loop Semi Join` with NO join-level qual; `Seq Scan on
orders` + o_orderdate range `Filter:`; `Index Scan using
idx_lineitem_orderkey_fkidx` + `Index Cond: (l_orderkey =
o_orderkey)` + `Filter: (l_commitdate < l_receiptdate)`. Against
`pg-q4.txt`: the semi-join core is placement-identical (no join
qual; qual as inner-scan `Filter:`). Remaining divergences are
the recorded F9 expectation, not this round: PG is parallel
(Finalize/Gather/Partial/Sort) while goopg stays serial
(Sort/HashAggregate), and row/width estimates differ (known
TPC-DS-scale row-estimate gap family; NO gate looks at rows=).
`pg-plan-parity-diff.py` will STILL report shapediff on Q4 — a
hand-pass here must never be read as tool parity.

## 3. Values bind (DESIGN §3.3)

- TPC-H digest: 24/24 OK, `-diff` vs
  `bench/tpch/baseline-digests.txt` → **24 MATCH, VERDICT: PASS**
  (values, not row counts). Q4 rows verified directly on r48h2:
  {10695, 10372, 10620, 10541, 10606} (5 rows).
- SF0.5 sweep (`GOOPG_BIN=/tmp/pp2/goopg-r48h2-sweep` private
  path, report `sweep-20260910-120016.txt`): **PASS=95 (57
  ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0**;
  status-delta vs previous: no verdict changes, runtime −0.1%.
  Plan channel (non-blocking): 14 changed vs the R47-era
  baseline — the Half-1 removals; Half-2 contributes zero
  (corpus A/B above).
- Spotcheck gate: Q12=2/Q13=34 PASS (Q13=34 is the pinned
  2026-08-26-load expectation; Q12=2 structurally invariant).
  First digest attempt OOM-killed the server on Q18 under default
  GC env; re-ran clean at `GOGC=100 GOMEMLIMIT=12GiB` (the
  documented bench recipe) — 24/24 OK. Server age is irrelevant
  to a values digest (deterministic), so no A/B confound.
- Units: `internal/optimizer` + `internal/executor` suites green;
  `RALPH_PRECOMMIT_SCOPE=units` pre-commit green, zero FAIL
  lines (pgbench smoke left to the commit hook).

## 4. Pre-existing conditions observed (not caused, not owned)

- `TestLeftJoinCrossRelationResidualReachesNLI` SKIPs ("leak route
  closed", tree `Project(Join{algo:NL})`) — verified identical at
  clean HEAD with Half-2 files set aside; the R25 decomposed arm
  now serves that shape before legacy `tryBuildNLI` fires.
- `Join Filter: (true)` from `exists_to_any.go:355-367`
  (DESIGN §4 follow-up): still corpus-zero, untouched.
- F10 closed: no test asserts `joinFilterRejected` counts.

## 5. Files

- `internal/executor/operators_explain.go` (both Filter arms)
- `internal/executor/explain_truefilter_skip_test.go` (new)
- `internal/executor/nli_semi_residual_exec_test.go`
  (placement pins added to the two shape tests)
- `internal/optimizer/nl_index_join.go`
  (`lowerSemiResidualToCond` + call site)
- `internal/optimizer/nl_index_join_cond_lower_test.go` (new)
- `internal/optimizer/unnest_indexkey_test.go`
  (S6 end-to-end updated to the new contract)
