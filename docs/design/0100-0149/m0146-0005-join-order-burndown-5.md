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
