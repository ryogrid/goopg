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
