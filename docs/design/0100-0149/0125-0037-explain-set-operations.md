# M0125-0037(i) — set operations were opaque to EXPLAIN

**Status:** stage (i) implemented 2026-07-31; stage (ii) (the planner half) open.
**Milestone:** M0125 (TPC-DS timeout class).
**Filed by:** `M0125-0026` (`analysis/m0125-0026-timeout-plans/README.md` §"C4").

## The defect

`internal/executor/operators_explain.go` drives all three EXPLAIN renderers —
TEXT (`walkPlanFiltered`), TEXT+ANALYZE (`walkPlanAnalyzeFiltered`) and
JSON/XML/YAML (`planToJSON`) — from exactly two functions: `describePlan` for
the node label and `planChildren` for recursion. Neither had a
`*planner.SetOp` case.

The consequences compounded. `describePlan` fell through to its
`fmt.Sprintf("%T", n)` default and printed the raw Go type name
`*planner.SetOp`; `planChildren` returned `nil`, so the node's `Left` and
`Right` branches were never walked. Any plan whose set operation sat near the
root was therefore truncated at that line. TPC-DS Q5, Q18 and Q67 each rendered
as a **four-line plan with the entire query body invisible**:

```
 Limit  (cost=0.00..0.00 rows=100 width=0)
   ->  Sort  (cost=0.00..0.00 rows=1 width=0)
         Sort Key: ca_country, ca_state, ca_county, i_item_id
     ->  *planner.SetOp  (cost=0.00..0.00 rows=1 width=0)
(4 rows)
```

That is why M0125-0026's plan capture could not classify those three queries at
all, and why this item was sequenced ahead of every per-class fix: each of those
reads its evidence through this instrument.

## PG's vocabulary, captured rather than inferred

The spellings below were taken from PostgreSQL 18.3 on the TPC-DS reference
cluster (`:65438`), not read out of `explain.c` alone:

| SQL | PG 18.3 plan |
|---|---|
| `A UNION ALL B` | `Append` with two children — **no SetOp node exists** |
| `A UNION B` | `HashAggregate` (`Group Key:`) over `Append` |
| `A INTERSECT [ALL] B` | `HashSetOp Intersect [All]`, two direct children |
| `A EXCEPT [ALL] B` | `HashSetOp Except [All]`, two direct children |

`explain.c` (`ExplainNode`, `T_SetOp`) confirms the split: `sname` is `"SetOp"`,
`pname` is `"HashSetOp"` for `SETOP_HASHED`, and the command token
(`Intersect` / `Intersect All` / `Except` / `Except All`) is appended to the
**text** line but emitted as separate `Strategy` / `Command` properties in the
non-text formats. Verified against PG's own JSON output:

```json
{ "Node Type": "SetOp", "Strategy": "Hashed", "Command": "Intersect All", "Plans": [ … ] }
```

PG 18 also changed `SetOp` to take its two inputs directly (`plannodes.h`, and
the capture above shows two `Seq Scan` children with no intervening `Append`),
which happens to match goopg's binary `SetOp{Left, Right}` exactly.

## What landed

Four helpers in `operators_explain.go`, wired into `describePlan`,
`planChildren` and `planToJSON` so all three renderers move together (Hard-won
Rule #2):

- `setOpRendersAsAppend` — true for `All && Op == SetOpUnion`. This is also the
  shape `planner.go:2495`/`:2545` build for partition / inheritance expansion,
  so getting this branch right is what keeps a partitioned scan from printing
  as a set operation.
- `setOpNodeName` — `Append`, else `HashSetOp <cmd>`.
- `setOpCommandName` — mirrors `explain.c`'s `SETOPCMD_*` text.
- `setOpAppendBranches` — flattens a left-deep UNION ALL chain. goopg builds
  `a UNION ALL b UNION ALL c` as `SetOp(SetOp(a,b),c)`; PG plans one `Append`
  with three children. Without flattening, Q5's five-branch union would render
  five `Append` levels deep and stop diffing against PG. Only ALL-union links
  are absorbed — an `INTERSECT` or `EXCEPT` in the chain keeps its own line.

`planToJSON` additionally emits PG's `"Node Type": "SetOp"` + `"Strategy"` +
`"Command"` triple instead of folding the command into the name, matching the
capture above.

### The one deliberate divergence

goopg has a single fused node where PG has two: `operators_setop.go` streams for
UNION ALL and buffers a multiset otherwise, so the UNION-distinct case is one
node that both appends and deduplicates. It prints **`HashSetOp Union`** — a
spelling PG never emits, chosen because PG's `SetOpCmd` has no `Union` member
and so the label cannot be confused with a PG node that means something else.
Rendering it as `Append` would claim a dedup that is not happening; rendering it
as `HashAggregate` would claim an aggregate with two children. Ledger row
2026-07-31 carries the two-node `HashAggregate`-over-`Append` shape as the
outstanding PG behaviour.

## Acceptance — and what the expanded plans immediately revealed

Re-captured the seven set-op-bearing members of the timeout class on the SF=0.5
goopg cluster (`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/`,
plain `EXPLAIN` only, nothing executed). Q5 went from 4 plan lines to 128, Q18
to 91, Q67 to 94, Q14 to 815. All three previously unclassifiable queries now
have a class:

- **Q5 → C1 + C2.** `Nested Loop (CROSS)` between the
  `store_sales ∪ store_returns` Append and `date_dim`, once per channel, with
  the `d_date` range predicate sitting on the join's `Filter:` instead of on
  the scan — so `date_dim` is costed at its full 73,049 rows where PG's
  scan-level filter yields 8. PG hash-joins on `ss_sold_date_sk = d_date_sk`.
- **Q18, Q67 → a class the earlier capture could not see.** Both are `ROLLUP`
  queries, and goopg expands the grouping sets into a **UNION ALL of one
  independent aggregate branch per level**, each branch re-running the entire
  join subtree. Q18: 4 branches, 5 `Multi-Way Hash Join`s, 5 full
  `catalog_sales` scans (720,657 rows each). Q67: 8 branches, 9 MHJs, 9 full
  `store_sales` scans (1,439,608 rows each). PG computes every level in **one**
  pass — Q18's plan is a single `GroupAggregate` with five stacked
  `Group Key:` lines, Q5's PG plan a `MixedAggregate` with `Hash Key` lines.
  This is filed as `M0125-0040`; it is the most likely proximate cause of both
  timeouts and of the Q18 warm regression tracked as `M0125-0033`, and neither
  ROLLUP query contains a `union all` in its SQL text, which is why the class
  was invisible until the set-op node itself became readable.

## Tests

`internal/executor/explain_setop_test.go` — the branches render as children and
are indented under the parent; a three-branch UNION ALL flattens to exactly one
`Append`; each of the five command spellings; and the JSON property triple.

## Not in this stage

Stage (ii), the planner half, remains open: the set-op node appears on one side
of a `Nested Loop (CROSS)` in Q8, Q14, Q54 and Q71, so the join-order DP's
inability to see through it is the likely proximate cause of C1 at those sites.
Its acceptance is `Q5` completing and matching `5|OK|100`.

Two rendering divergences are recorded in the deferral ledger rather than fixed
here, because both would churn every existing plan line: the UNION-distinct
node shape above, and the fact that goopg's child indent steps 2 columns per
level where PG steps 6 (`strings.Repeat("  ", depth)` in `walkPlanFiltered`),
which makes a deep plan's arrows nearly vertical.
