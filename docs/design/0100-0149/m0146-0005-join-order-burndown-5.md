# M0146-0005 (part 5): slices 71+ — index-only coverage and the needed set

Continuation of [m0146-0005-join-order-burndown-4.md](m0146-0005-join-order-burndown-4.md)
(slices 56-70), split per the design-doc size rule (D3). Same task and census
family.

## Recon 71: M0146-0005bs — an EXISTS body's star voids the needed set (not landed)

After slice 70, TPC-DS Q94's only divergence is its anti-join probe: PG
plans `Index Only Scan using web_returns_pkey on web_returns wr1`, and
goopg an Index Scan. Q94 writes the probe as
`NOT EXISTS (SELECT * FROM web_returns wr1 WHERE ...)`. PG's
`simplify_EXISTS_query` (subselect.c) discards an EXISTS target list
before planning, so the star reads nothing. goopg's needed-column
collector (pathindexonlyneed.go) walks the body's targets, meets the
`StarExpr` and declines, which makes the whole statement's needed set
unknown. No index-only path, and no narrowing, is then offered anywhere
in the statement.

I tried dropping a targets list made only of stars and constants from an
EXISTS body, in both collectors. A PG 18.3 oracle unit test (anti join
over `SELECT *`, count 19) went green. On TPC-DS it was a net loss, so the
change was reverted:

- Q94 kept its plain `wr1` probe. The fire set shows the plan changed,
  but the probe did not become index-only. So the anti-join probe over
  the pulled EXISTS leaf comes from a route this producer does not reach,
  or the leaf is not bare there. Not yet traced.
- Q10 and Q35 (SF0.25), and Q35 at SF1, fell back to the unsearched
  syntactic plan with `seam-decline reason=residual-hits-pad`.
  - Their needed sets became known, so narrowing padded columns. Slice 70
    pads per alias.
  - `searchedResidualHitsPad` (narrowoutput.go) is keyed by NAME. For a
    residual holding a correlated sublink (Q35's
    `exists(...) or exists(...)`), it declines when any padded column's
    name is needed anywhere. A column padded on one date\_dim alias is
    needed on another, so the check fires.
  - `boundaryPaddedNames` has no qualifier to attribute by, because a
    `SchemaColumn` carries none.

Prerequisites before the EXISTS change can land (filed as M0146-0005bs):

1. Make the residual pad check alias-aware. Carry each padded slot's
   qualifier from the boundary filler, which already computes it, and
   test it with `neededColumnNamedFor`.
2. Trace which producer builds Q94's anti-join probe over the pulled
   EXISTS leaf, and give it the index-only arm.

Evidence: `analysis/m0146/m0146-0005/recon71/`.

## Slice 72: M0146-0005bs — the EXISTS star lands, with a residual pad check that reads outer references

Recon 71's first prerequisite is done here, and the EXISTS change lands.

- `residualColumnRefsByName` (narrowoutput.go) no longer marks the walk
  partial when the residual holds a correlated sublink. A correlated plan
  reads the row it runs under only through its `OuterColumnRef`s, and each
  of them carries the column's name.
  - Every outer reference whose level reaches this scope or beyond is
    reported by name, including those in nested sublinks
    (`walkPlanExprsDeep`).
  - Over-reporting (a reference that a lateral binder inside the plan
    satisfies, or one aimed further out) only makes the fallback fire more.
  - A nameless outer reference still makes the walk partial.
- Q35's `exists(...) or exists(...)` residual reads only
  `c.c_customer_sk`. The old partial walk fell back on any statement-needed
  column padded anywhere, and slice 70's per-alias pads made that fire.
- `existsBodyForColumns` (pathindexonlyneed.go) drops an EXISTS body's
  target list when it holds only stars and constants, as simplify\_EXISTS\_query
  does. A `SELECT *` inside EXISTS therefore no longer voids the
  statement's needed set.

Tests:

- `TestExistsStarProbeIsIndexOnly` uses a PG 18.3 oracle: an anti join over
  `NOT EXISTS (SELECT * ...)` probes `p_pkey` index-only, count 19.
- `TestResidualColumnRefsByNameSublinkScopes` now pins the new contract:
  total, reporting the outer reference, and partial only for a nameless one.

Movement:

- None on the instruments. Five fires (Q10 Q16 Q35 Q69 Q94) change plans
  with no category movement at either scale, and all execute.
- The `residual-hits-pad` seam declines that recon 71 hit are gone: the
  sweep shows only its baseline `leaf-count` declines.
- ea-ratchet stays at 10. TPC-H plans are identical. Units, spotcheck, the
  sweep (96/96) and the arm (24/24) pass. Regress is unchanged apart from
  join.sql's flaps.

Q94 is still not index-only, and the cause is now traced. The `wr1` probe
is a parameterised SKIP path (`index.parameterised.skip`). The join binds
`wr_order_number`, the second key of `web_returns_pkey`, and PG 18's
matching node is an index-only skip scan. goopg's `IndexOnlyScan` has no
skip prefix, which is why `createIndexScanPlan` refuses one. The index-only
arm of `addOneParameterizedSkipPath` needs executor skip support in
`indexOnlyScanOp` first. Filed as M0146-0005bt.

Evidence: `analysis/m0146/m0146-0005/slice72/`.

## Slice 73: M0146-0005bt — index-only skip probes

Slice 72 traced TPC-DS Q94's remaining divergence to its `wr1` anti-join
probe. The probe binds `wr_order_number`, the SECOND key of
`web_returns_pkey`, so it is a PG 18 skip scan, and PG makes it index-only
under the same `check_index_only` rule as any index path. goopg's
`IndexOnlyScan` had no skip prefix, so the parameterised skip producer
offered only the heap-fetching probe.

- The skip enumeration moved out of `indexScanOp` into a shared
  `btreeSkipEnum` (new btree\_skip.go):
  - `init` evaluates the bound columns once per Rescan, and reports a NULL
    bound as an empty scan;
  - `next` returns the next prefix group's (lo, hi) probe, for both the
    tuple and the blob key formats.
  
  `indexScanOp` delegates to it (its lazy cursor-per-group drive is
  unchanged). `indexOnlyScanOp.Rescan` gains a skip branch that runs one
  bounded range scan per group, materialised eagerly like every other
  index-only shape.
- `IndexOnlyScan.SkipPrefix` is added on the plan node. The index-only
  branch of `createIndexScanPlan` accepts a parameterised skip path, and
  `setNLIProbeKeys` keeps `Keys` under a skip prefix. EXPLAIN renders only
  the bound quals under their own columns, in `formatIndexOnlyCond` and in
  `probeKeyEqualities` (the Memoize/NLI qual reader).
- `addOneParameterizedSkipPath` gains the index-only arm of slice 69
  (bare leaf, covering index, per-alias needed set, allvisfrac costing and
  covered widths).

Test: `TestIndexOnlySkipProbe` covers two PG 18.3 oracle shapes over
`q_pkey(k1, k2)` probed on k2. The anti join gives count 19; the inner join
gives count 18 and `sum(k1)` 36, reading the skipped key column back out.
It fails with the producer arm removed.

Movement (fire set):

- PLAN-PARITY match SF0.25 35 → 36, SF1 27 → 28 (Q94).
- CATEGORIES-EXCL-MATCH scan-type SF0.25 30 → 28, SF1 36 → 34 (Q94, and
  Q16's probe).
- ea-ratchet stays at 10. TPC-H plans are identical. Units, spotcheck, the
  sweep (96/96) and the arm (24/24) pass. Regress btree\_index,
  create\_index, index\_including, join, subselect and select are
  unchanged.

Evidence: `analysis/m0146/m0146-0005/slice73/`.

## Slice 74: M0146-0005bu — a correlated scalar sublink on one relation is that relation's restriction

TPC-DS Q1, Q30 and Q81 filter
`ctr1.ctr_total_return > (SELECT avg(...) * 1.2 FROM ctr ctr2 WHERE
ctr1.k = ctr2.k)`. PG 18.3 places this on the `ctr1` CTE Scan:
`distribute_qual_to_rels` takes a clause's relids from `pull_varnos`, and a
correlated SubPlan contributes its testexpr Vars plus the outer Vars its
parameters carry. All of those name `ctr1`, so the clause is a base
restriction there (Q30 SF1: `rows=109` on the scan). goopg kept every
correlated sublink conjunct of a multi-relation scope as a join residual
(M0146-0015a), so the filter sat on the top Nested Loop.

- `correlatedScalarSublinkLeaf` (local\_filters.go) admits a conjunct that
  `conjunctLocalEligibility` declines when all of the following hold:
  - its only sublinks are scalar;
  - its same-scope columns all lie in binding 0;
  - every outer reference in its inner plans names binding 0 of this scope,
    and none reaches past it.

  EXISTS and IN stay excluded because the post-planning EXISTS→ANY and
  unnest passes read them off the top qual holder.
- The admission is built on `walkExprRefs`, which is complete over Expr
  types and fails closed. The inner plans are judged by `planEscapesBy`,
  which is `planHasEscapingOuterRef` with the escape rule passed in (the
  historical rule is `outerRefReachesPast`). Its Visit dispatch is pinned in
  the walker inventory as a classifier, next to its sibling.
- **Offset 0 only.** Binding 0's leaf coordinates are the FROM-cumulative
  ones, so neither `localizeExprToLeaf` nor the unlowered inner plan moves a
  coordinate. The rebase needed for any other binding is ledgered.
- **Join-width fix in the unnest driver.** goopg's scalar-aggregate unnest
  (not a PG transform) still decorrelates such a leaf qual. It appends the
  aggregate's columns to the host, which is harmless under the top Project
  but shifts every coordinate past a join input. A plain-table repro counted
  0 where PG counts 993. `unnestKeepingWidth` now projects a widened join
  input back to its original columns.

Tests:

- `TestCorrelatedSublinkIsBaseRestriction`: the PG 18.3 shape, with the SubPlan
  on the r1 CTE Scan and count 801, read with the unnest post-pass off.
- `TestUnnestedLeafSublinkKeepsJoinWidth`: counts 993/993/801 with the pass
  on. It fails, reading 0, without `unnestKeepingWidth`.

Movement (fire set):

- The qual is placed as in PG for Q1 (SF0.25), Q30 and Q81 at both scales.
  TPC-H Q20's `ps_availqty > (SubPlan)` now filters the partsupp index scan,
  as in PG.
- PLAN-PARITY match is unchanged: SF0.25 36, SF1 28, TPC-H 11.
  CATEGORIES-EXCL-MATCH aggregation-strategy goes SF0.25 18 → 17 (Q1 no
  longer unnests at SF0.25). SF1 join-order goes 60 → 62 and scan-type
  34 → 35: with 109 driving rows instead of 327, the customer probe flips
  to a bitmap scan.
- The two differences left on Q30/Q81 are both outside this slice:
  - goopg prices the customer index probe at 12.25 where PG has 7.61. This
    is the parked M0142-0005c multiplier, and it is what makes the bitmap
    win.
  - EXPLAIN numbers `SubPlan 1` where PG numbers `SubPlan 2`. PG's plan ids
    count the CTE plan first; filed as M0146-0005bv.
- goopg still charges a sublink one operator in `qualEvalOps`; PG adds the
  SubPlan's per-call cost (`cost_subplan`). That is ledgered.
- Gates: units, spotcheck, sweep 96/96, arm 24/24 and ea-ratchet (10)
  pass. Regress subselect, with and join are unchanged (join shows only its
  known row-order flap).

Evidence: `analysis/m0146/m0146-0005/slice74/`.
