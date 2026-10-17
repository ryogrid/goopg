# M0146-0133 — a sorted Unique keeps its input's pathkeys for the ORDER BY above

Status: done 2026-10-09 (c12f5dc92). Parent: M0146-0014a.

## Problem

TPC-DS Q49 is a UNION (with dedup) of three channels, ordered by
`channel, return_rank, currency_rank, item` and limited to 100 rows.

- **PG** dedups with `Unique -> Sort (('web'::text), web.item, ...)`. The
  ORDER BY above it is an `Incremental Sort` with `Presorted Key:
  ('web'::text)`.
- **goopg** built the same Unique, then put a full `Sort` above it.

The parity diff showed `sort-strategy` at both scales.

## PG behaviour

`create_upper_unique_path` (pathnode.c) sets `pathnode->path.pathkeys =
subpath->pathkeys`. A Unique returns the first row of each run of equal
keys in the order it reads them, so it keeps its input's order.
`create_ordered_paths` then sees a path presorted on the ORDER BY's first
key and builds an Incremental Sort.

## Change

`inputNodePathkeys` (upperorderedinput.go) works out the ordering a
finished input node delivers, walking down through nodes that keep their
input's order. It had no arm for `*DistinctOn`, the node EXPLAIN prints
as `Unique`, so it claimed no ordering.

The new arm descends into a non-hashed `*DistinctOn`'s child when the
child's schema agrees, as the walk already does for `Filter` and `Limit`.
The executor (`distinctOnOp`) streams its input and compares adjacent
keys, so it keeps that order.

A hashed `DistinctOn` keeps no order and stops the walk. That form is
the HashAggregate goopg uses to unique-ify a semijoin's inner side.

## Verification

- **Test.** `TestInputNodePathkeysCrossesASortedUnique`: a sorted Unique
  delivers its Sort's keys, and the hashed form claims none.
- **TPC-DS fire set.**
  - Q49 at both scales goes from `[join-order, scan-type,
    sort-strategy]` to `[join-order, scan-type]`. The top is now PG's
    `Incremental Sort (Presorted Key: ('web'::text)) -> Unique`.
  - Q54 changes at both scales, with its categories unchanged. Its
    `my_customers` DISTINCT leaf now publishes its order, and the
    `store` join moves from a Hash Join to a Nested Loop with the
    `ca_county = s_county` Join Filter, which is PG's shape there.
  - CATEGORIES-EXCL-MATCH sort-strategy: SF0.25 20 → 19, SF1 24 → 23.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only `join`'s nondeterministic unordered
  `ss1 left join ss2 on true` row order changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **Q49's remaining `[join-order, scan-type]`.** Each channel's
  sales ⋈ returns ⋈ date_dim join is ordered differently from PG's.
  The M0146-0014a census routed only Q49's first divergence (this task),
  so the next one goes to the next parity-closure sweep.
