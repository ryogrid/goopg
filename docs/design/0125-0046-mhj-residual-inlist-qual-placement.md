# M0125-0046 — MHJ residual-qual placement: the IN-list was never in `mh.Filters` to begin with

Status: **landed** (2026-08-01). Filed 2026-08-01 by the M0125-0035 closure as
arm (b); evidence `docs/design/0125-0035-c2-single-table-qual-placement.md` §2
and §6(b), ledger row 2026-07-31 (M0125-0035, "InExpr disqualifier").

Related: `0125-0004-q75-join-residual-evaluation-order.md` (the binary-join
sibling pass this extends), `0125-0035-c2-single-table-qual-placement.md`
(C2), `tpcds-round2-fixes/README.md` §3.4 (RC-1b coordinate discipline),
`0125-0013-mhj-posmap-filtered-leaf.md` (Filter-wrapped MHJ members).

---

## 1. The item as filed — and the misdiagnosis in it

The fix_plan row said: *"`pushSingleSourceFiltersIntoMHJTables` disqualifies
any conjunct containing an `InExpr`"*, and the ledger's resume point said to
relax that veto in its `visitColumnRefNodes` outer-callback.

That diagnosis is **wrong at HEAD**, and was wrong at the time it was
recorded. The planner's shared walker (`pushdown.go::walkColumnRefsImpl`) has
admitted literal IN-lists since the M0061 fix (`InExpr` with `Plan != nil`
vetoes; a literal list walks `Operand` and `List`), and an isolated probe of
`pushSingleSourceFiltersIntoMHJTables` with an `InExpr` conjunct in
`mh.Filters` pushes it correctly — the RC-1b commit had already given
`shiftColumnRefs`/`cloneExprForShift` their `InExpr` arms.

The measured behaviour (§2 of the 0035 doc: `ca_state IN ('IL','TX','ME')`
stranded on the MHJ node, `customer_address` hashed whole at 50,000 rows, MHJ
emitting 96,562 rows for an 11,049-row answer) is real. The mechanism is not
a walker veto:

**The conjunct is never in `mh.Filters` at all.** `mh.Filters` is populated
by `collectMultiHashTables` capturing *extras that `pushOneConjunct` had
already AND'd onto a binary join's Predicate*. A WHERE restriction that
stayed in the residual `*Filter` above the join chain — which is where a
non-equality single-table conjunct normally lives — is still in that Filter
when MHJ packing replaces the chain, and:

- `pushSingleSourceFiltersIntoMHJTables` reads **only `mh.Filters`**;
- `pushSingleSideQualsIntoInnerJoinInputs` (the binary sibling that DOES read
  the residual Filter) required `f.Child` to be a `*Join` — a
  `*MultiHashJoin` child fell through and the whole pass declined
  (ledger row 2026-07-31 "MHJ/NLI declined as descent paths" recorded the
  adjacent shape).

So the defect class is a **coverage hole between two passes**, not a walker
veto. The executor half of the filed diagnosis, however, was accurate: see §4.

## 2. Planner fix — `pushResidualQualsIntoMHJTables`

`pushInnerJoinInputQuals` (inner_join_qual_pushdown.go) now dispatches on the
Filter's child: `*Join` keeps the existing path; `*MultiHashJoin` goes to the
new `pushResidualQualsIntoMHJTables`, which for each conjunct of
`splitAnd(f.Predicate)`:

1. attributes it to the unique `mh.Tables[t]` whose cumulative-offset range
   covers every `ColumnRef` (`mhjResidualConjunctTable` — same offset
   arithmetic as the `mh.Filters` pass, because both expressions live in
   MHJ-output coordinates once `remapWithBindings` has run);
2. shifts a **clone** to leaf-local coordinates (`shiftConjunctForInput`,
   fail-closed `cloneExprRefs`);
3. descends with the existing `pushConjunctIntoSubtree`, which supplies the
   terminal wrapping (`Filter{LeafLocal: true}`), the exprEqual idempotence
   guard, and the LeafLocal coordinate-convention check.

The binary arm's four load-bearing properties carry over verbatim; the one
that needs no analog is the outer-join analysis, because
`collectMultiHashTables` only packs `JoinTypeInner` chains — every MHJ member
is a preserved side by construction.

Attribution is fail-closed on the modern walker (`walkExprRefs` +
`scopeVeto`): an unenumerated Expr kind, an `OuterColumnRef`, a `FuncCall`
(no provolatile model — matching `innerJoinPushTarget`'s veto), any subquery
(`InExpr.Plan != nil` carries a `slotInnerPlan` child), an out-of-range
index, a positional name mismatch, or refs spanning two members all decline,
leaving the conjunct in the residual — slower, never wrong. Property 2 means
the residual keeps its copy even on success, so a decline anywhere downstream
can never lose the restriction.

### The LeafLocal stamp on the sibling pass

`pushSingleSourceFiltersIntoMHJTables`'s own wrapper now carries
`LeafLocal: innerJoinPushLeafScan(child)`. Its predicate always WAS in
leaf-local coordinates; the flag just says so. Without it, the two passes
could not compose: when the `mh.Filters` pass (runs first, planner.go:1166)
wraps a member and the residual pass (planner.go:1176) then attributes a
second conjunct to the same member, `pushConjunctIntoSubtree`'s
coordinate-convention guard compares `LeafLocal` against
`innerJoinPushLeafScan(child)` and declines on mismatch. Safe to stamp
because no cumulative-space remap pass can reach a wrapper inside
`mh.Tables`: `applyJoinTreePosMap` and `remapPosMapAfterRewrite` both stop at
the `*MultiHashJoin` node without recursing into `Tables[i]` (the RC-1b
design), and `reconcileNLILayout`/`nl_index_join` only become *more*
conservative for a LeafLocal wrapper.

## 3. Measured effect

SF0.5 probe (the 0035 §2 query, `customer ⋈ customer_address ⋈
customer_demographics WHERE ca_state IN ('IL','TX','ME')`, serial):

- before: `customer_address` hashed whole (50,000 rows), MHJ emits **96,562**;
- after: scan-level `Filter: (ca_state = ANY ('IL','TX','ME'))` on the
  member scan, MHJ emits **11,049 = the answer**, count byte-identical to the
  PG oracle on :65438.

TPC-H plan-diff vs `m0125-0044-after`: 5/22 DIFFER (Q2 Q3 Q10 Q11 Q21), every
diff the same shape — `+ Filter:` lines appearing under MHJ member scans,
zero change to join structure, scan order, or worker counts. This is PG's
`distribute_restrictinfo_to_rels` placement reaching MHJ members for
non-equality restrictions (`o_orderdate < …`, `c_mktsegment = 'BUILDING'`,
`l_receiptdate > l_commitdate` on the correct `l1` alias, `r_name = 'EUROPE'`
inside Q2's correlated subplan MHJ, `n_name = 'GERMANY'` in both of Q11's
MHJs). The equality-with-index cases were already reaching scans through
`rewriteScanInputsWithSingleTablePredicates`; this closes the rest.

## 4. Executor half — the sibling walker really did veto `InExpr`

`internal/executor/multi_hash_join.go::walkColumnRefs` (the classifier behind
`partitionFilters`) is the documented mirror of the planner walker, and IT
vetoed every `InExpr`, sending literal IN-lists in `mh.Filters` to
`leafFilters` — evaluated only after every chain step binds — instead of the
step where their columns first bind. Per Hard-won Rule #2 both siblings moved
together:

- `InExpr` with `Plan != nil` still vetoes; a literal list walks `Operand`
  and `List` (`Args` accompany `Plan` only, so the veto covers them);
- the kinds the planner walker has that this one lacked are now enumerated
  (`CastExpr`, `IsNullExpr`, `IsBoolExpr`, `IsDistinctFromExpr`,
  `CollateExpr`, `RowExpr`) — before, their operands' refs were invisible, so
  e.g. an `IS NULL`-only filter read as "constant" and was evaluated at
  probe time where its column may not be bound;
- constants are enumerated as explicit no-op leaves, and the **`default` arm
  is now `onOuter()`** — an unenumerated kind routes to `leafFilters`, which
  is always safe (all tables bound), merely latest. Silence must mean
  "refuse", not "safe" (tpcds-round2-fixes §0).

## 5. Verification

- `internal/planner/mhj_residual_qual_pushdown_test.go`: push lands leaf-local
  on the right member with the residual copy intact (property 2); re-walk
  idempotence; cross-member conjunct declines; the two MHJ passes compose
  through one LeafLocal wrapper.
- `internal/executor/mhj_inlist_filter_test.go`: IN/NOT-IN dimension
  predicate over the RC-1b geometry end-to-end; the walker contract (literal
  IN admitted, subquery IN vetoed, unenumerated kind vetoed).
- Gates: units pre-commit PASS; planner suite (incl. the exprwalk inventory
  gate — `mhjResidualConjunctTable` pinned as `nonRecursiveClassifier`);
  `tpch-spotcheck` RESULT=PASS (Q12=2 Q13=35); SF0.5 subset probe of 15
  MHJ-heavy queries (7 10 26 27 30 31 34 35 47 50 69 72 73 79 96):
  PASS=15 MISMATCH=0 CKMISMATCH=0 (Q72 straddled its 300 s cap on the
  nightly-saturated host and PASSed solo at 600 s; run under FORCE=1, no
  timing claimed); plan-diff 5/22 with row-count proof: Q3=11521 Q10=20501
  Q11=819 Q21=405 all equal to `ci/batch/tpch-row-anchors.csv`, and Q2 —
  which has no anchor — md5-identical (455 rows) between a HEAD-baseline
  binary and the fixed binary on the same data.

## 6. Deliberately NOT done

- **Duplicate, not move.** PG moves the restriction to `baserestrictinfo` and
  the join residual never sees it; goopg keeps the residual copy (property
  2), so EXPLAIN shows the filter twice and the MHJ re-evaluates it per
  output row. Idempotence-by-construction and join-order stability were
  judged worth the per-output-row re-evaluation. Ledger row 2026-08-01.
- **FuncCall conjuncts decline** (no provolatile model) — same gap already
  recorded for the CTE-body pass; the row there stands.
- **A conjunct descending a *Join* spine still stops above an MHJ.** This
  change covers a Filter DIRECTLY above the MHJ (the shape every measured
  case had); `pushConjunctIntoSubtree`'s `*Join` arm still has no
  `*MultiHashJoin` descent case. Ledger row 2026-07-31 (M0125-0035
  "MHJ/NLI declined as descent paths") stays open, now with the offset
  arithmetic available to share (`mhjResidualConjunctTable`).
