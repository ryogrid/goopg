# 02 — Premise audit: is a PG left-deep cascade really the same thing as an MHJ?

## 1. In PostgreSQL: yes, and the proposal states it correctly

PG's `HashJoin` node (`postgres/src/backend/executor/nodeHashjoin.c`) has an inner `Hash` child
that is drained into a `HashJoinTable` of `MinimalTuple`s, and an outer child that is pulled one
tuple at a time. Nothing between two stacked `HashJoin`s is materialised: the upper join's outer
subplan *is* the lower join's `ExecProcNode`. A left-deep cascade with the fact table as the
outermost outer therefore streams each fact tuple through N-1 hash lookups, holding only the N-1
dimension hash tables in memory. That is, structurally, what `MultiHashJoin` does. **The
proposal's core claim about PostgreSQL is correct and is the right thing to aim at.**

Two PG details worth carrying: (a) PG's intermediate tuple is a `TupleTableSlot` composed by
`ExecHashJoin`'s `econtext` — no row concatenation; (b) PG's `Datum` is 8 bytes, so even where PG
does copy, it copies 6× less than goopg (`internal/executor/datum.go:119`:
`const _ uintptr = 48 - unsafe.Sizeof(Datum{})`).

## 2. In goopg: **no — and there are TWO copy sites, on TWO different execution paths**

> **This section was rewritten after agent review (finding F1, [10](10-agent-review.md)).** The
> first draft named a single line and generalised it to all execution. goopg has **two** plan→
> operator paths, they materialise in **different places**, and which one a query takes depends
> on which plan nodes it contains. Any design that names only one of them is designing for half
> the server.

### 2.0 Two execution paths, and which queries take which

| path | entry | how a join's probe input arrives |
| --- | --- | --- |
| **slab / fast-iterator** — the live server path | `executor.BuildFastIterator` (`internal/server/dispatch.go:2839`, `:3691`) → `opTreeSlab.buildRec` (`internal/executor/executor.go:425`) | probe child is an `opNodeOperator` returning a `*Slot`; `Slot.Row()` (`opnode.go:111`) is **a view, not a copy** |
| **legacy `Build`** | `executor.Build` (`executor.go:21`), reached for any subtree under an unmigrated node via `OpAdapter` | probe child is a `joinOp` returning a `*VirtualSlot`; `VirtualSlot.Row()` (`slot.go:159-164`) **allocates and copies** |

`buildRec` migrates only `SeqScan, Filter, Project, Limit, Sort, Update, Delete, Insert, Join`
(verified: the `case *planner.` arms between `executor.go:425` and `:546`). **`Aggregate` is not
migrated**, so a plan topped by `Sort → HashAggregate → …joins…` — which is *every* TPC-H
star query in this bundle's scope (Q5, Q7, Q9, Q10, Q18, Q21; see the plan snapshots) — falls to
`OpAdapter` and runs its whole join subtree under **legacy `Build`**. Short OLTP-shaped joins,
by contrast, run on the slab.

So both sites below are live, on disjoint query populations.

### 2.1 Site A — legacy path: `slotRow` at the seam between two stacked joins

goopg's binary hash join is *nearly* PG-shaped. `joinOp.openLazyHashJoin`
(`internal/executor/operators_join_agg.go:494`) drains only the build side
(`drainRowsBounded`, :555 / :621) and streams the probe side (`openProbeSide`, :510). The output
is a shared `VirtualSlot` composed over two persistent `MaterializedSlot`s
(`ensureLazyVirtual`, :1034), so there is no per-row row-concat allocation. All of that is
already right.

The defect is at the *seam between two stacked joins*:

```
internal/executor/operators_join_agg.go:1207-1215
    probeSlot, err := o.lazyProbe.Next()
    …
    r := slotRow(probeSlot)          // :1214
    o.lazyRow = r
```

`slotRow` (`internal/executor/slot.go:189`) calls `TupleSlot.Row()`. When the probe child is
another hash join, the slot it returns is a `*VirtualSlot`, and:

```
internal/executor/slot.go:159-164
func (s *VirtualSlot) Row() Row {
    out := acquireRow(len(s.cols))
    for i := range s.cols {
        out[i] = s.Get(i)
    }
    return out
}
```

`acquireRow` (`internal/executor/row_pool.go:42-53`) takes a pooled slice **and zeroes it**, then
every column is copied as a 48-byte `Datum`. `o.lazyRow` is then reassigned on the next probe row
with **no matching `releaseRow`**, so the pool does not recycle it and the copy is pure garbage.

**And it is not the only copy on this path.** Immediately after, the probe-side hash key is
evaluated through a *second* full-width copy into a scratch buffer, because
`evalHashKeyDatum` (`:960-969`) takes a `Row`:

```
internal/executor/operators_join_agg.go:1216-1241
    if o.lazyKeyRow == nil || len(o.lazyKeyRow) != w { o.lazyKeyRow = make(Row, w) }
    copy(o.lazyKeyRow[:o.lazyLW], r)          // (or [o.lazyLW:] when BuildLeft)
    kd, kok, kerr := o.evalHashKeyDatum(probeKeyExpr, o.lazyKeyRow)
```

So the legacy path pays **two** W-wide `Datum` copies per probe row per level. A fix that
removes only the first halves the traffic (review finding F2).

### 2.2 Site B — slab path: `fillFromTupleSlot`, and it copies twice too

On the slab path `slotRow(probeSlot)` at `:1214` is already free, because the child is an
`opNodeOperator` whose `Slot.Row()` returns `Row(s.Cells)` — a view (`opnode.go:111`). The copy
has simply moved one level down, into the kernel that publishes a join's output into the slab:

```
internal/executor/opnode.go:869-876
func joinOpKernelNext(n *OpNode, dst *Slot) error {
    slot, err := s.op.Next()
    …
    dst.fillFromTupleSlot(slot)
}

internal/executor/opnode.go:133-152 (fillFromTupleSlot)
    row := ts.Row()                  // <- VirtualSlot.Row(): acquireRow + W Datum copies
    if cap(s.Cells) < len(row) { s.Cells = make([]Datum, len(row)) }
    s.Cells = s.Cells[:len(row)]
    copy(s.Cells, row)               // <- and a SECOND W Datum copy
```

Two full-width copies plus one unreleased pooled row, per output row, per join level — the same
cost as the legacy path, at a different address.

### 2.3 The cost, in arithmetic

> Arithmetic estimate from data-structure sizes, **not a measurement**. No server was started for
> this bundle.

The copies are paid once per **probe-side** level, not once per level: a sub-join that sits on the
*build* side is drained by `drainRowsBounded` (which copies anyway, `spill.go:388-394`) and gains
nothing from any of this — see §2.5. For TPC-H Q9's MHJ subset — F ≈ 6.0 M `lineitem` rows
(`plan_snapshots/r5-default.txt:133`), output width growing toward ~40 columns, and taking the
2 probe-side seams the shape actually has:

    6.0e6 rows × (25 + 40) cols × 48 B × 2 copies ≈ 37 GB

of zero-fill + copy traffic **that the MHJ shape does not perform at all**, plus tens of millions
of pooled-row allocations the GC must sweep. Against a 12 GiB `GOMEMLIMIT` and `GOGC` behaviour
under `scripts/goopg-test-run.sh`, that is a plausible, arithmetic-grounded explanation of the
"memory-thrash, cancellation not honored" class in
`analysis/tpch-evolution-round5-int64-hashjoin-20260724.md` §6 — **without needing the
"per-row operator overhead" story doc 15 tells**, which the M0068/M0071-0014 tuple-slot work
and the int64 hash fast path (commit `0aeb7613`, 2026-07-23, *before* the 804 s measurement)
had already largely retired.

### 2.4 The consequence for the proposal

MHJ's advantage over goopg's cascade is **not** fusion of join logic. It is that MHJ composes
**one** `VirtualSlot` over N persistent per-table slots
(`internal/executor/multi_hash_join.go:265-291`, `tableSlots []*MaterializedSlot` +
`virtualOut *VirtualSlot`) and never materialises an intermediate. A cascade can have exactly
that property **without being fused**, if each level keeps its child's slot *as a source*
instead of copying it into a `Row`.

The lifetime argument. `joinOp` calls `o.lazyProbe.Next()` only when it has finished serving every
match for the current probe row (`nextLazy`'s outer `for` at `operators_join_agg.go:1153` pulls a
new probe row only after the inner match loop at `:1160-1190` drains), so the child's slot is
stable for exactly the window the parent reads it.

**Honesty note about the standing of that argument (review finding C7).** The first draft cited
`internal/executor/operator.go:49-64` as "the contract". That text is a *parenthetical historical
note* documenting the removal of `BorrowSemantics`/`Borrowable` in M0071-0015 — it is not a
normative section, and **nothing enforces it**. The lifetime argument above stands on the
control flow of `nextLazy`, which is checkable, not on a contract that exists only as a comment.
Stage 1 should therefore *promote* it to a real, asserted invariant (a debug-build check that a
source slot is not advanced while a composing slot is outstanding), not merely rely on it.

**Therefore the first move is not fusion. It is removing the materialisation at both sites.**
That is [08](08-staged-plan-and-gates.md) Stage 1. It is executor-only, plan-shape-neutral, and
it is independently valuable even if MHJ is never removed. Note the scope correction from
review finding F1: Stage 1 must address **`fillFromTupleSlot` on the slab path as well as
`slotRow` on the legacy path**, or it fixes nothing for slab-routed queries.

### 2.5 What the shape actually is — and it is not left-deep

The proposal's PG argument (§1) is about a **left-deep** cascade with the fact table outermost.
goopg's DP emits **bushy** trees, and the build side is chosen by size
(`buildLeft = lRows > 0 && rRows > 0 && lRows < rRows`, `internal/planner/bushy.go:1382`; also
`pushdown.go:300-310`, `planner.go:2277`). The codebase knows both shapes occur —
`parallel_hash_build.go:163-166` states it: *"The walk descends the PROBE side only. A hash join
nested on another join's build side is built as part of that build."*

Q5's committed plan (`plan_snapshots/r5-default.txt:62-71`) is the concrete case, and it is the
query with the worst regression in the whole evidence set:

```
->  Hash Join (INNER)  rows=402301450
  ->  Seq Scan on public.lineitem  rows=5999786
  ->  Hash Join (INNER)  rows=1991195     <- sub-join is the BUILD side (1.99M < 6.0M)
```

The deepest and widest level has its sub-join on the **build** side — correctly, because it is
the smaller side, which is exactly what PG would do — and slot chaining does nothing for it. The
two levels above it do have the sub-join on the probe side and do benefit. So the win applies to
the probe-side seams only, roughly 2 of 3 here.

Two consequences that must not be glossed:

1. §2.3's arithmetic counts probe-side seams, not levels (already corrected).
2. **The PG left-deep equivalence of §1 does not transfer to goopg's actual plans.** Stage 5's
   case rests on plan-shape parity with PG; a bushy goopg tree is not PG-isomorphic to a left-deep
   PG tree even after MHJ is gone. Stage 5 must therefore answer "does the DP produce a
   left-deep, fact-outermost shape for these queries at all?" — and today the answer is no.

## 3. The three claimed benefits, audited

### 3.1 "PG plan-shape parity becomes real, so `make plan-gate` and `pg-oracle-diff.sh` become meaningful" — **REJECTED as stated**

- `make plan-gate` (`Makefile:376-390`) builds `cmd/plan-snapshot` and diffs live goopg EXPLAIN
  against **a committed goopg baseline** picked by `ls -t plan_snapshots/*.txt | head -1`. It has
  never compared anything to PostgreSQL. The MHJ node cannot have been "always mismatching" it —
  the baselines *contain* MHJ lines (e.g. `plan_snapshots/m0076-baseline-ffc3429.txt:11`).
- `scripts/pg-oracle-diff.sh` runs the same SQL on goopg and PG 18.3 and diffs **psql output
  rows**. It is a results oracle, not a plan oracle.
- So removing MHJ does not *unlock* a dormant gate. It makes a **new** gate *possible*: a
  goopg-vs-PG structural EXPLAIN diff. That gate does not exist and must be written
  ([08](08-staged-plan-and-gates.md) Stage 4). Counting it as a free benefit of the change is
  the single largest overstatement in the proposal.
- Independently, goopg EXPLAIN cannot be structurally compared to PG today for a second reason:
  every goopg node prints `cost=0.00..0.00 … width=0`
  (`internal/executor/operators_explain.go:378`, quoted in
  `docs/design/cost-model/README.md`) — only `rows=` is real. A PG-parity plan gate must
  normalise cost and width away, which also means it cannot detect cost regressions. State that
  limitation up front.

### 3.2 "It escapes doc 15's dead end, because fusion no longer has to win on cost" — **ACCEPTED, and it is the strongest argument**

`docs/design/cost-model/15-mhj-in-cost-driven-star-shapes.md` records a real, closed dead end:
at a 100× materialisation penalty the MHJ still cost 416673 vs the cascade's 393420 (`WIN=false`)
for Q9's subset, and the doc's own conclusion is that "the blocker underneath is the join ORDER,
not the MHJ cost". A decision made at execution time, on a shape the planner already chose, is
simply not subject to that argument. This is sound.

But note the corollary, which the proposal does not state: **if fusion never has to win on
cost, then the cost model never learns that the cascade is expensive.** Cost-driven order will
keep choosing orders whose cascades are bad, and fusion will paper over some of them and not
others. [06](06-cost-model-interaction.md) treats this as a required companion work item, not
an optional one.

### 3.3 "The qualification predicate and the 651-line executor already exist, so this is closer to a relocation than a new implementation" — **REJECTED**

`internal/executor/multi_hash_join.go` cannot be relocated as-is. Five blocking defects, each
verified:

| # | defect | evidence | why it blocks reuse |
| --- | --- | --- | --- |
| 1 | **Unbounded build, no `WorkMem`, no spill.** MHJ drains each build child with `drainRowsCtx` (`multi_hash_join.go:184`) into an unbounded `[]Row`, then builds a map on top, holding both. `WorkMem` appears nowhere in the file. | grep: `ctx.WorkMem` used at `operators_join_agg.go:485,531`; absent in `multi_hash_join.go` | A fused operator that inherits this **loses `joinOp`'s spill** (`drainRowsBounded`, `spill.go:342`). That is a memory regression dressed as a performance win. |
| 2 | **The spanning-tree walk silently drops join edges.** `multi_hash_join.go:130-165`: greedy source-order walk; `if buildTbl == probe \|\| visited[buildTbl] { continue }` skips a cycle-closing edge, and `if !found { break }` (:162) exits with `len(keySteps) < len(Keys)`. The surplus equijoins are **not** re-homed as residual filters. | code | Dropped equijoin ⇒ **over-emission** ⇒ a silent row-count regression, the exact failure class [07](07-risk-register.md) is built around. |
| 3 | **Unreached tables get an arbitrary hash key.** `keyColByTable[i] == -1` falls into the fallback at :190-205 and hashes on the first key column mentioning the table — contributing no matches. | code | Silent row *loss*. |
| 4 | **A whole re-coordinatisation layer.** `rewriteMultiWayChain` sorts `Tables` by catalog OID (`internal/planner/bushy.go:1790-1830`) and is then followed by `buildMHJPosMap` (`bushy.go:2349`) / `remapColumnRefsAfterRewrite` (`bushy.go:1854`), which rewrite `ColumnRef.Index` across the rest of the tree. **Correction from review (F9):** the OID sort is not itself the bug — its own comment (`bushy.go:1790-1795`) says it exists *to match* the replaced binary tree's FROM-order schema; the skew it repairs is the **bushy DP's DFS walk order**. The hazard is the remap layer that the flattening makes necessary. | code + `bushy.go:1790-1795` | A runtime fusion **must not** need a remap layer at all — see [03](03-semantic-contract.md) §2. Reusing MHJ's composition means reusing the flatten-then-remap round trip, which is precisely what we are trying to delete. |
| 5 | **Incomplete residual-filter walker.** `walkColumnRefs` (`multi_hash_join.go:379-408`) has no arm for many `Expr` kinds and falls through silently; an unhandled filter is classified probe-only and evaluated **too early**. This is the walker class `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md` was written about. | code + that doc | Wrong-time evaluation of a residual is a correctness bug on LEFT/anti shapes and a performance bug on INNER. |

What *can* be reused is the **idea** and the **qualification predicate's shape** (doc 15 §2 —
≥3 tables, composite-free, single-column base-table keys, `#edges == #tables−1`). Everything
else is a rewrite against `joinOp`'s semantics. Budget accordingly.

## 4. The benefit the proposal does not claim, which is the best one

Deleting `MultiHashJoin` as a plan node deletes a large, actively-bleeding surface in the
**planner**, not the executor:

- `rewriteMultiWayChain`, `collectMultiHashTables`, `rewriteMHJInputsWithSingleTablePredicates`,
  `buildMHJPosMap`, `remapColumnRefsAfterRewrite` / `remapPosMapAfterRewrite`,
  `attachUnusedCrossEdges`'s MHJ-only global-coordinate path
  (`internal/planner/bushy.go:1416` comment) — hundreds of lines whose entire job is to repair
  index skew introduced by **flattening a bushy DP tree into an OID-ordered table list and remapping every
  `ColumnRef` back** (`bushy.go:1790-1795` explains the sort; `:1854` / `:2349` do the repair).
- **28 non-test `case *MultiHashJoin:` arms across 15 files** (counted by grep): `unnest.go` 7,
  `bushy.go` 4, `mhj_input_rewrite.go` 3, `parallel.go` 2, `operators_explain.go` 2, and one each in
  `executor.go`, `subplan.go`, `subplan_lower.go`, `subplan_lower_walk.go`, `subplan_cost.go`,
  `pushdown.go`, `inner_join_qual_pushdown.go`, `nl_index_join.go`, `exists_to_any.go`,
  `view_privilege.go` — each an independent chance to forget the node in a new pass.
- The bug class is live, not historical: `exists_to_any.go:476` warns that "MultiHashJoin OID
  re-sort or bindings remap can leave it stale", and the tip-of-branch commit `23a077ae`
  ("an OR-ed IN operand keeps its final index, not a stale one") is another instance.

**That is the honest business case for this bundle: not speed, but the permanent removal of an
index-skew bug generator.** Speed must merely not regress.

## 5. Restated proposal (what this bundle actually designs)

1. Make the goopg binary hash cascade genuinely pipelined (slot chaining) so it has MHJ's
   memory profile in a PG-identical shape. **Executor-only, no plan change.**
2. Measure MHJ-on vs MHJ-off at *identical join order* for the first time.
3. Only if a gap remains, add runtime fusion as a strictly-optional execution strategy under a
   kill switch, designed fresh against `joinOp` semantics.
4. Then, and only then, delete the `MultiHashJoin` plan node and its planner machinery.
5. Then build the goopg-vs-PG plan-shape gate the proposal assumed already existed.
