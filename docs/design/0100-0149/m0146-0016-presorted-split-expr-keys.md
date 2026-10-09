# M0146-0016 — presorted split admits non-column group keys

Status: landed 2026-09-27.

## Problem

M0146-0003 S6's sorted row-transport split (`Finalize GroupAggregate ->
Gather Merge -> Sort -> Partial HashAggregate`, the presorted arm of
`gather_grouping_paths`, planner.c:7704-7724) keys the per-worker Sort
and the Gather Merge with ONE `[]SortKey` list whose `Expr`s are
positional `*ColumnRef`s into the transport row: position `k.Pos`
carries `GroupExprs[k.Pos]`'s value.

`transportGroupSortKeys` built those refs by cloning each group
expression's `*ColumnRef` and rebinding `Index = k.Pos`. A group
expression that was not a bare column (a `substr`, an `extract`, a
literal like TPC-DS Q76's `'store'::text`) had nothing to clone, and the
arm declined — the position would have evaluated correctly, but nothing
could render an honest `Sort Key:` name for it.

PG carries arbitrary group expressions on this arm. The measured corpus
consumers (M0146-0001 reference captures, both TPC-DS scales): Q62 and
Q99 group by `substr(w_warehouse_name,1,20)` and PG elects the presorted
family; goopg could only file the hashed split.

## Decision

The transport row already carries the computed expression value at the
key position — evaluation was never the blocker; naming was. Two facts
make the widening exact rather than heuristic:

1. `Aggregate.Output()` names every group slot via `groupExprName` →
   `targetMeta` (PG's FigureColname), so `agg.schema[k.Pos]` is a stable,
   non-empty name for the position.
2. `sortGroupKeySource` (R66 Arm S, operators_explain.go) resolves a
   `Sort Key:` ColumnRef *positionally* through the child aggregate's
   `GroupExprs` — `sch[idx].Name == col.Name` is only an identity guard —
   and renders the group expression itself, S18-wrapped. PG prints the
   same thing: `Sort Key: (substr((w_warehouse_name)::text, 1, 20))`.

So the synthesized ref `ColumnRef{Index: k.Pos, Name: sch[k.Pos].Name,
Type: sch[k.Pos].Type, SourceTableIdx: sch[k.Pos].SourceTableIdx}` is
both the correct evaluator (position) and the honest name (the schema
slot's). Bare-ColumnRef group exprs keep the original clone path — their
own Name/Type/SourceTableIdx ride along, byte-identical behaviour.

## Fail-closed edges

The arm declines (`ok=false`) when a clause position is out of range, the
output schema is missing/short, or the slot is unnamed — same posture as
`partialGroupKeyRefs`. The unchanged refusal test now exercises exactly
this edge (a schema-less fixture must decline, because the position
cannot be named honestly).

## What this does NOT include

- PG's *other* presorted variant — sorted-input `Partial GroupAggregate`
  over a worker `Sort` of the input (the shape PG elected for Q62/Q99).
  goopg files only the hash-then-sort partial; electing the sorted-input
  partial is a separate admission task. The `sort-strategy` census
  category on Q62/Q99 records this residue.
- Election changes: the arm competes in `add_path` as before; nothing
  forces it. Q76's column keys were always admissible and its plan is
  unchanged.

## Verification

See `analysis/m0146/m0146-0016/` (README + gates.txt + fireset
artifacts). Q62/Q99 elect `Finalize GroupAggregate -> Gather Merge ->
Sort -> Partial HashAggregate` with `Sort Key: (substr(...))`, rows
match the oracle, all four gate stamps PASS, fireset `introduced=none`
at both scales.
