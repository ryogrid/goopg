# R48 — semi JoinQual placement + `Filter: (true)` drop

Named by R47 DESIGN §4. Two independent halves; Filter-half first
(de-risks the census), either order otherwise. Status: DESIGN (Step 0
closed 2026-09-10, live PG :65432 vs s2 tree).

## 0. Step 0 — grounded facts (no models, only readings)

**The pair.** TPC-H Q4, GUCs pinned
`work_mem='64MB'`, `max_parallel_workers_per_gather=4` both sides:

- PG 18.3 (`:65432`, captured 2026-09-10): `Nested Loop Semi Join`
  with NO join-level qual. The EXISTS inner qual lives on the probe:
  `Index Scan using idx_lineitem_orderkey_fkidx ... Index Cond:
  (l_orderkey = orders.o_orderkey)` + `Filter: (l_commitdate <
  l_receiptdate)`.
- goopg (s2 tree, `/tmp/pp2/oc-tpch-s2.txt` Q4 section):
  `Nested Loop Semi Join (cost=0.00..1141.32 ...)` +
  `Filter: (l_commitdate < l_receiptdate)` +
  `Filter: (true)`, inner `Index Scan` with `Index Cond:` only.

Q4's join is a `*NestedLoopIndexJoin` (two `Filter:` lines is the
NLI render shape — own `Predicate` line + attached-wrapper line,
`operators_explain.go:832-844`; a plain `*Join` residual would print
`Join Filter:`, `:826-828`).

**Half 1 provenance (all 40 stray lines are wrappers).** Census on
the s2 corpora: `Filter: (true)` 6× TPC-H + 34× TPC-DS, every
instance the second line of an NLI pair or a lone line under a
hash/semi join — i.e. always the attached-`Filter` slot
(`walkPlanFiltered`, `:441-458`), never a `Predicate` slot:

- `combineAnd([])` is nil (`pushdown.go:530`), and the legacy NLI
  builder's residual is `combineAnd(leftover)`
  (`nl_index_join.go:763-771`) while the fused arm's is
  `joinPredicate(...)` → `combineAnd` (`createplanjoin.go:478`).
  An empty residual is nil on both arms — no `Filter: (true)`
  comes from an empty predicate.
- The wrappers are unnest scaffolding left deliberately:
  `pushConjunctsBelowSemiAnti` sets `BooleanConst{true}` "instead
  of removing the wrapper" (`unnest.go:404-414`, caller `:516`
  returns the wrapper as-is); `:3157`, `:3281`, `:3469`, `:3570`,
  `:4309` keep a true-wrapped `Filter` "so downstream code that
  recurses through Filter still finds the join". Removal would
  pull that scaffolding; the renderer already collapses
  wrappers, so the renderer is the safe fix point.
- Census scope, stated exactly (review F3): the 40-line census
  covers the attached-`Filter` slot only. The `Predicate` slot
  has its own known true-producer — `exists_to_any.go:355-367`
  sets `*Join*.Predicate = BooleanConst{true}` when all
  conjuncts are consumed (nil would spell CROSS join), which
  renders as `Join Filter: (true)` via `formatJoinFilter`
  (`operators_explain.go:992-1007`). Corpus-zero (`Join
  Filter: (true)` greps 0 on both s2 corpora), so it does not
  touch this round; filed as follow-up (§4), not owned here.

**Half 2 provenance (legacy hoist vs path-arm Cond).**
`IndexScan.Cond` (`plan.go`, `IndexScan.Cond` doc) is the designed
home for inner quals on an NLI probe: leaf-local coords,
evaluated per heap tuple the probe returns, rendered as the
scan's `Filter:` (PG-identical — `operators_explain.go:722-741`
joins `Cond` with any attached wrapper). "Only the NLI arm sets
it": the fused builder absorbs `LeafLocal` wrappers via
`absorbableLeafCond` (defined `createplanindex.go:224`, called
`createplannl.go:273/371/462`). The legacy `*Join → NLI` rewrite
(`tryBuildNLI`, `nl_index_join.go:318`) instead hoists inner
Filters into `residualPred` — the D6.3b blowup the `Cond` doc
names (per-pair re-eval of a per-row clause). Q4's semi comes
from the unnest post-pass (`unnestSubqueriesInPlan`, after the
search), so it takes the legacy rewrite — consistent with the
observed join-level residual + nil `Cond`.

Pre-search already splits the sides: `planner.go:3271-3291`
(the M0063-0005 LEFT-join ON-clause pushdown, Q13 — cited here
as a COORDINATE precedent only, not a semantic one:
`classifyConjunctSide`, `pushdown.go:372-400`, +
`shiftColumnRefsBy(c, -leftWidth)`) — the shift the lowering
reuses. `classifyConjunctSide` can return `sideOutOfScope`
(-1) for `OuterColumnRef`/out-of-range refs; those stay in the
residual (fail closed).

## 1. Half 1 — drop `Filter: (true)` at the renderer

In `walkPlanFiltered`'s `*optimizer.Filter` arm: when
`f.Predicate` is `*BooleanConst` with `Value: true` (and ONLY
that — nil predicates untouched), recurse into `f.Child`
carrying the INCOMING `attachedFilter`/`attachedFilterNode`
unchanged (pass-through, so a real outer filter above a
true-wrapper still renders; stacking collapse otherwise
unchanged). Nothing else moves: executor tree, estimates, and
all producers untouched. The host line re-prices from the
wrapper carrier to the child carrier — that IS the
PG-faithful carrier (PG has no such node).

## 2. Half 2 — lower inner-only semi residuals to `IndexScan.Cond`

In `tryBuildNLI`, after `residualPred` is computed: split its
conjuncts by side (same `classifyConjunctSide`-family test the
pre-search split uses); conjuncts referencing only the inner
schema move onto the inner probe as `IndexScan.Cond` with
ColumnRefs shifted to leaf-local (the `-len(outerSchema)`
shift, mirroring `planner.go:3281`); the remainder stays as
`NLI.Predicate`. Gates on the move:

- SEMI/ANTI only. LEFT keeps its residual unconditionally: a
  moved qual stops filtering null-extended rows (wrong rows,
  not a perf question). INNER untouched this round (output
  includes inner columns; same mechanism, separate census).
- Inner must be a bare `*IndexScan` (the `innerBase` the
  builder already extracts); IOS/bitmap inners decline (their
  `Cond` fields exist but are a later slice).
- INSERTION ORDER (review F2 — a wrong-rows trap lives here):
  the move runs BEFORE `indexOnlyNLIInner(inner, residualPred,
  …)` (`nl_index_join.go:791-794`), so the IOS check sees
  `Cond` set and declines (`:1496-1498` declines on
  `inner.Cond != nil`; the `IndexOnlyScan` it builds carries no
  `Cond` even though the field exists with executor support).
  The naive order is SAFE (decline keeps the `IndexScan` +
  `Cond` — correct); a mixed residual the move renders
  outer-only stays IOS-declined (missed narrowing, no
  regression). What the implementer must NOT do is "fix" that
  by checking IOS first and setting `Cond` after — that drops
  the qual silently. Pinned by test 4's mixed-residual case.
- Executor support already exists (fused arm's per-probe `Cond`
  eval); the move changes WHERE the clause evaluates (per inner
  row, as costed) not WHAT matches — values gates arbitrate.
- Estimate-audit flag (review F5): `EstimateRows(*IndexScan)`
  ignores `Cond` (`cardinality.go:47-52`), so the relocated
  `Filter:` line shows unfiltered probe rows — unlike PG's
  post-qual count. The fused arm already lives with this;
  values unaffected.

This converges legacy with the path arm (rule #2: sibling paths
must agree) and with PG's `distribute_restrictinfo_to_rels`
(MOVE, not goopg's usual copy — no `PushedBelow`, no
double-eval, no double-charge).

## 3. Success tests (all must hold)

1. Stray-line census 40 → 0 (`Filter: (true)` in s2 corpora);
   the ONLY other moving EXPLAIN lines are host-line
   re-pricings and NLI-residual lines relocated onto inner
   scans — each relocation adjudicated toward PG's placement
   (inner `Filter:` under `Index Scan`, join-level line gone).
2. Shapes byte-identical except the two halves' line moves;
   ZERO EXTRA flips (no previously-matching query newly
   diverges; `match` count not a criterion per conjunction
   rule).
3. Values bind: TPC-H digest 24/24 MATCH, SF0.5 sweep
   `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0`, spotcheck
   Q12/Q13 canonical counts.
4. Units + pre-commit suite green; new pins: renderer-skip
   (true-wrapper above join; true-wrapper between real filter
   and join — outer still renders; nil predicate untouched),
   lowering (semi inner-only moves; outer-referencing and
   `sideOutOfScope` stay; LEFT declines; non-bare inner
   declines; mixed-residual semi → stays `IndexScan`, residual
   reduced, `Cond` set, no IOS — the F2 order pin).
5. Q4 section adjudicated line-by-line against the archived
   Step-0 pair (`pg-q4.txt` vs `goopg-q4-s2.txt` in this dir —
   review F1; the live capture behind `pg-q4.txt` is recorded
   in its header). Parallel shape aside — Gather/Finalize is
   the parallelism category, not this round — AND that scoping
   is recorded as an expectation, not a pass: goopg Q4 stays
   serial while the PG capture is parallel, so
   `pg-plan-parity-diff.py` will STILL report shapediff on Q4
   after both halves (review F9). A hand-pass here must never
   be read as tool parity.
6. Shaped-query expectations for the other half-1 lines
   (review F8 — census + values alone, no per-query PG
   adjudication): Q16/Q18 hash nodes keep bare `Hash Cond:`
   (their lone stray lines are the attached slot, not `Join
   Filter:`); Q21's outer node is NLI-Anti, so its real
   residual stays as the `Predicate` line. The report names
   each query's expected post-half-1 text.

## 4. Non-goals / follow-ups (named, not owned)

- INNER/LEFT NLI residuals; plain-`*Join` residuals;
  IOS/bitmap-`Cond` inners; `pushInnerJoinInputQuals`-family
  copies (their double-eval is a separate R).
- `Join Filter: (true)` from `exists_to_any.go:355-367`
  (review F3 — corpus-zero, Predicate slot, renderer fix
  cannot touch it).
- `absorbableLeafCond` call-site cites drifted to
  `createplannl.go:273/371/462` (review F6 — fixed here).
- `joinFilterRejected` observability (review F10): verified
  no test asserts per-query counts; moved conjuncts stop
  incrementing it, accepted.
- Firing micro-rule; K100; node/path agg-cost unification
  (R47 REPORT follow-up); TPC-H fixture re-capture (owner).
