# 02 — Premise audit: what the proposal gets right, wrong, and misses

## 1. RIGHT — goopg's binary hash join is already a pipelined probe

The proposal's core architectural claim checks out in the code.

- `joinOp.openLazyHashJoin` (`internal/executor/operators_join_agg.go:494-507`) drains the
  **build** side and then opens the **probe** side.
- `buildLazyHashTable` (`operators_join_agg.go:524-690`) with the default `BuildLeft=false`
  drains `o.right` via `drainRowsBounded(o.right, budget)` (`:645`) and returns
  `probeIsLeft = true`.
- `openProbeSide` (`:510-522`) then `Open`s the **left** child and stores it as `o.lazyProbe`
  — it is never drained.
- `nextLazy` (`:1108+`) pulls one probe row at a time from `o.lazyProbe`.

So a left-deep tree `((((fact ⋈ d1) ⋈ d2) ⋈ d3) ⋈ d4)` with dimensions on the right builds
four small hash tables at `Open` and then streams fact rows through all four without ever
materialising an intermediate. **That is structurally identical to what MHJ does.** The
proposal is correct that MHJ is a hand-fusion of a shape goopg already supports.

`planner.Join.BuildLeft` (`internal/planner/plan.go:835`) is the switch that decides this,
and `probeSideIsLeft` (`internal/executor/parallel_hash_build.go:110-117`) is the canonical
statement of the rule.

## 2. RIGHT — MHJ is a genuine PG-compatibility defect

`internal/executor/operators_explain.go:1386-1390` renders
`Multi-Way Hash Join (N tables)`, a string PostgreSQL will never emit. Every structural plan
comparison against PG is guaranteed to mismatch on any query that packs. That part of the
motivation is sound and is not disputed anywhere in this set.

## 3. SPLIT VERDICT — fusion escapes doc 15's *stated objection*, but not its *binding constraint*

> **This section is the one place where the two independent design passes reached opposite
> conclusions, and the disagreement is worth preserving rather than smoothing over.** Pass A
> ruled the claim FALSE. Pass B ruled it ACCEPTED and called it "the strongest argument" for the
> proposal. Both are right about different halves, and the synthesis is the split below.
>
> **What fusion genuinely escapes.** Doc 15's proximate finding was that an MHJ candidate cannot
> be made to *win a cost comparison* against the equivalent binary cascade without distorting all
> 22 queries. A decision taken at execution time, on a shape the planner has already chosen, is
> simply not subject to that argument. Pass B is correct: this objection is escaped cleanly, and
> it is a real advantage of the third option over doc 15's v2.
>
> **What it does not escape.** The sentence immediately after that objection says the objection
> was not the blocker. Pass A is correct about the consequence, and the synthesis pass found the
> decisive confirmation independently: **Q5, the worst regression in the whole evidence set, has
> no MultiHashJoin in its plan at all** ([01 §2.3](01-motivation-and-measured-evidence.md)).
> Fusion could not have helped it under any implementation.
>
> **Net:** the claimed benefit is real in the abstract and worth ≈ zero on the queries doc 15 was
> trying to fix. It must not appear in any stage's success criteria.
>
> **Corollary that the proposal does not state (from pass B).** If fusion never has to win on
> cost, then *the cost model never learns that the cascade is expensive.* Cost-driven order will
> keep choosing orders whose cascades are bad; fusion will paper over some and not others.
> [07](07-cost-model-interaction.md) treats correcting the cost model as required companion work,
> not an optional extra.

Doc 15's status block says, verbatim:

> **The blocker underneath is the join ORDER, not the MHJ cost.** … The DP correctly prefers
> joining the *filtered* `part` early (its green filter reduces 6M→322K, making later joins
> cheap) — which fractures the MHJ-eligible subset. Flipping that preference is order surgery
> that changes every cost-driven query's plan.

The proposal answers a *cost* objection ("the MHJ never wins on cost") with a *timing*
change ("decide at execution time, so it needn't win on cost"). But the sentence immediately
after that objection says the cost objection was **not the blocker**.

Concretely: if the DP emits `((part ⋈ lineitem) ⋈ (supplier ⋈ nation)) ⋈ partsupp`, then

- the top join's left input is a 4-table intermediate, not a base relation;
- the fusion predicate ([05](05-qualification-predicate.md)) requires an unbroken left-deep
  chain of INNER hash joins over base-relation build sides, and this tree is bushy;
- fusion declines, the plan runs exactly as it does today, and the 804 s is unchanged.

**Runtime fusion can only accelerate plans the planner already shaped correctly.** It is
therefore *not* a substitute for the order work; it is a complement whose value is zero on
precisely the queries doc 15 was trying to fix. This must be stated in the implementation
plan as a hard dependency (Stage 3), and no stage may claim Q5/Q9/Q21 as a target.

## 4. MISSED — two mechanical per-row costs in the cascade, removable without fusion

This is the most actionable finding in this set.

### 4.1 The cascade re-materialises the accumulated row at every level

> **Corrected after adversarial review (finding F2 in [11](11-adversarial-review.md)).** The
> first draft of this section located the cost at `nextLazy`'s `r := slotRow(probeSlot)`.
> That is wrong **on the live server path** and the correction changes what Stage 0a must
> touch. The cost is real; the site is one layer down.

There are two builders ([04 §1](04-fusion-site-and-data-structures.md)), and they have
**different per-row allocation profiles**. Both must be measured separately.

**Live path (`BuildFastIterator` → `buildRec`).** A `Join`'s children are `opNodeOperator`
(`internal/executor/executor.go:535-547`; the type at `internal/executor/opnode.go:329-352`),
whose `Next()` returns `&w.dst`, a `*Slot`. `Slot.Row()` is `return Row(s.Cells)`
(`opnode.go:110-111`) — **zero copy**. So `nextLazy`'s `slotRow(probeSlot)` allocates
nothing here.

The materialisation is in the slab kernel instead:

```go
func joinOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*joinOpState)
	slot, err := s.op.Next()
	…
	dst.fillFromTupleSlot(slot)      // opnode.go:868-876
	return nil
}
```

and `Slot.fillFromTupleSlot` (`opnode.go:129-150`) does:

```go
row := ts.Row()                     // *VirtualSlot ⇒ acquireRow + per-column copy
…
copy(s.Cells, row)                  // second copy
```

`VirtualSlot.Row()` (`slot.go:159-166`) calls `acquireRow`
(`internal/executor/row_pool.go:42-53`), which `Get`s from a width-keyed `sync.Pool` and
**zeroes** the slice, fills it column by column — and the acquired row is then **dropped
without `releaseRow`**. `grep releaseRow internal/executor/*.go` finds calls only in
`operators_ddl.go`, `operators_storage.go` and `operators_index.go` — never in
`operators_join_agg.go` or `opnode.go`.

So per emitted tuple per cascade level the live path pays: a `sync.Pool` Get, a width-wide
zeroing loop, a width-wide column-by-column copy into the pooled row, a second width-wide
`copy` into `dst.Cells`, and a pool entry thrown away.

**Legacy path (`Build`).** Here the child `joinOp` returns its `*VirtualSlot` directly and
`nextLazy`'s `slotRow(probeSlot)` **is** the `acquireRow` site, as the first draft said. This
path is what `EXPLAIN ANALYZE` runs (`operators_explain.go:57-64` calls `Build` under
`withInstrumentation`) — see [06 §2](06-explain-and-plan-shape.md) and finding F6.

Either way, MHJ performs **zero** such materialisations: its `tableSlots`/`virtualOut` design
(`multi_hash_join.go:265-310`) is the M0071-0014 Stage D-2 optimisation that the binary path
received for its *own output* but not for its *input*.

Magnitude, stated precisely (finding F14): `acquireRow` only pools widths
`<= maxPooledRowWidth` (`row_pool.go:23,43`); wider rows are plain `make`. A 40-column
accumulated row is inside the pooled band, so the per-tuple-per-level cost is pool
Get + 40-entry zeroing + 40 `Datum` (48-byte) writes + a 1920-byte `copy`, not a heap
allocation. For 6M probe rows × 3 levels that is ~18M pool round-trips and ~2 × 2.3 GB of
`Datum` traffic. Large, but **it is a bandwidth cost, not an allocation cost** — the Stage 0c
measurement must be read with that in mind, because the go/no-go for the whole proposal rests
on this number.

**Fix without fusion (Stage 0a, revised):** add a `*VirtualSlot` fast path to
`Slot.fillFromTupleSlot` that reads `v.Get(i)` straight into `s.Cells`, skipping `acquireRow`
and the second copy entirely. That is ~5 lines, needs **no** slot-lifetime reasoning, and
removes one of the two copies plus the whole pool round-trip. Only if measurement says the
remaining single copy still dominates should the more invasive "hold the child's slot"
variant be attempted — and that variant carries finding F7's hazard (children do not return
a stable slot object).

### 4.2 The cascade memcpy's a scratch key row per probe row

Still in `nextLazy`:

```go
w := o.lazyLW + o.lazyRW
if o.lazyKeyRow == nil || len(o.lazyKeyRow) != w {
	o.lazyKeyRow = make(Row, w)
}
…
copy(o.lazyKeyRow[:o.lazyLW], r)
copy(o.lazyKeyRow[o.lazyLW:], nullRight)
```

The buffer is reused across rows (M0054-0005b already fixed the *allocation*), but the
**copy** is per row and is `O(leftWidth + rightWidth)`. Its only purpose is to let
`evalHashKeyDatum` resolve a `ColumnRef.Index` in the merged coordinate space. The same
build loops do the same thing (`:~600`, `:~660`).

**Fix without fusion:** evaluate the key expression against a `VirtualSlot` whose sources are
`{probeSlot, nullOtherSideSlot}` — identical index space, zero copy. Stage 0b.

### 4.3 Why this matters for the decision

If Stage 0a+0b close most of the per-row gap, then **the entire fused-operator project is
unnecessary**: the plan-shape parity benefit can be taken by simply flipping
`mhjPackingEnabled` to `false`, with no new operator, no new semantic contract, and no new
class of silent-row-loss bug. That would be by far the best outcome available, and it costs
one measurement to find out. Hence Stage 0 is mandatory and blocking in
[09](09-staged-implementation-plan.md).

## 5. WRONG — the gate-meaningfulness claim

- `make plan-gate` (`Makefile:376-390`) picks the newest `plan_snapshots/*.txt`, requires a
  reachable goopg on `PLAN_HOST:PLAN_PORT`, and runs `cmd/plan-snapshot diff`. It is
  **goopg-vs-goopg regression detection**, not PG comparison. It SKIPs (exit 0) when there is
  no baseline or no server — the repository's own memory records that it "silently SKIPs".
- `scripts/pg-oracle-diff.sh` header (`:1-44`) states it runs SQL against both engines and
  diffs "the normalised output", with normalisations listed for psql timing lines, blank
  lines, whitespace and prompts. It is a **result** oracle, not a plan oracle.

Removing MHJ therefore does not "make these comparisons meaningful". It **unblocks building a
new gate** — a structural EXPLAIN diff between goopg and PG 18.3 — which does not exist and
would be new work ([06 §4](06-explain-and-plan-shape.md)). That is a real benefit; it is just
not free, and the plan must budget it.

## 6. HALF RIGHT — "closer to a relocation than a new implementation"

The 651-line executor is real and the qualification predicate exists. But the operator
consumes a `*planner.MultiHashJoin` whose `Keys` are **pre-resolved by-index**
`MultiHashKey{LeftTable, LeftCol, RightTable, RightCol}` coordinates computed by the planner
(`bushy.go:1782-1800`, resolved via `findScanByColName`, `bushy.go:1731-1751`). Relocating the
operator means re-deriving those coordinates **at operator-build time** from `*planner.Join`
nodes whose keys are `Expr`s in the *joined* coordinate space.

That derivation is the exact code family that has produced this project's worst bugs:

- the layout/`posMap` remapping machinery (`bushy.go:2189-2400`, `buildMHJPosMap` at `:2348`);
- doc 13's "composite-NLI layout reconciliation" desync;
- the M0125-0008 bug documented in `internal/planner/plan.go:869-886`, where
  `rewriteMultiWayChain` re-sorted a subtree in place and left a semi/anti join's cached
  schema a "stale *permutation* of the real layout", so an ancestor key landed on the wrong
  column and `EXISTS … AND NOT EXISTS …` returned **more** rows than either conjunct alone.

So the relocation is not low-risk plumbing; it is a move of the highest-risk code in the
planner into a new coordinate system. [04 §4](04-fusion-site-and-data-structures.md) proposes
the one mitigation that actually removes the risk class: **derive nothing by name; require the
telescoping-width identity and fail closed if it does not hold.**

## 7. MISSED — fusing removes spill

The binary path drains its build side with `drainRowsBounded(op, budget)`
(`internal/executor/spill.go:342+`), where `budget = ctx.WorkMem` (default 512 MiB,
`operators_join_agg.go:~530`), and **spills to a temp file** past the budget
(`spill.go:365-380`).

MHJ drains its build children with `drainRowsCtx(child, ctx)`
(`multi_hash_join.go:~190`; `drainRowsCtx` is `operators_join_agg.go:3351-3380`), which has
**no budget and no spill** — it accumulates `[]Row` unbounded.

> **Corrected after review (finding F5).** The first draft added "and additionally deep-copies
> every row" as if that were a distinguishing defect of MHJ. It is not:
> `drainRowsBounded` **also** deep-copies — `dup = make(Row, len(row)); copy(dup, row)`, or
> `cloneRowOwned(row)` when `rowHasArena(row)` (`internal/executor/spill.go:388-399`). The
> copy is *required*: without it a hash-table entry would alias a producer's reused scan
> buffer (the M0097-0058 corruption class, cited in `drainRowsCtx`'s own comment at
> `operators_join_agg.go:3368-3372`). The real and only defect of MHJ's build path is
> **no budget and no spill**.

A fused operator that inherits MHJ's build loop therefore **loses spill on every build side**.
Given that the failure mode this whole line of work is trying to fix is *memory thrash*, that
is a regression in exactly the wrong dimension. The contract in
[03 §8](03-semantic-contract.md) makes `drainRowsBounded` mandatory for every fused level.

## 8. Verdict

| claim | verdict |
| --- | --- |
| A left-deep cascade with fact-outermost already streams like MHJ | **TRUE**, verified |
| MHJ is a PG-compat defect worth removing from the plan space | **TRUE** |
| Runtime fusion escapes cost-model doc 15's dead end | **FALSE** — doc 15's blocker is order, which fusion does not touch |
| Fusion makes `plan-gate` / `pg-oracle-diff` meaningful | **FALSE** — those gates do not compare plans to PG; a new gate would have to be built |
| Fusion is "closer to a relocation" | **HALF TRUE** — the operator moves, but the key-coordinate derivation is new and is the highest-risk code family in the repo |
| Cost-driven is 22 % faster on non-MHJ queries | **NOT SUPPORTED** by the raw evidence; it is 12 % slower on the completing set |

**Recommendation: ADOPT WITH MODIFICATION.** Take the direction. Insert Stage 0 before
anything else. Treat order as an explicit external blocker. Keep MHJ as a plan node until
Stage 4, which is conditional on measurement.

---

# Grafted from the second independent design pass

The sections below were produced by the second pass over the same brief and are carried here
because the first pass did not reach them. Each was re-verified by the synthesis pass against
source; the verification transcript is
[evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt). The
second pass's chapter in full is retained at
[evidence/panelB-02-premise-audit.md](evidence/panelB-02-premise-audit.md).

## 9. Which execution path a query takes is decided by whether it has an Aggregate

§4.1 establishes that goopg has two operator builders with different per-row profiles. It does
not say **which queries take which**, and that turns out to be decidable and decisive.

`opTreeSlab.buildRec` (`internal/executor/executor.go:425-546`) migrates exactly these plan
nodes to the slab (verified by enumerating its `case` arms):

```
SeqScan  Filter  Project  Limit  Sort  Update  Delete  Insert  Join
```

**`Aggregate` is not among them.** Any subtree under an unmigrated node falls to `OpAdapter` and
runs under legacy `Build`. So a plan shaped `Sort → GroupAggregate → …joins…` — which is *every*
TPC-H star query in this bundle's scope — runs its **whole join subtree under legacy `Build`**,
while short OLTP-shaped joins run on the slab.

Consequences the implementation plan must own:

1. Stage 0a's two variants are not "one primary, one optional". `0a-legacy` is the one that
   governs the analytic queries this work exists for; `0a-live` governs the pgbench path the
   pre-commit hook measures. **Both must be measured separately** and neither result may be
   generalised to the other.
2. Any A/B run through `EXPLAIN ANALYZE` measures the legacy path
   (`operators_explain.go:57-64` calls `Build`), which for these queries is the *right* path —
   but for OLTP shapes it is not the path production uses. See [06 §2](06-explain-and-plan-shape.md).

## 10. goopg's DP emits BUSHY trees — the PG left-deep equivalence does not transfer as stated

The proposal's PG argument is about a **left-deep** cascade with the fact table outermost. goopg's
DP does not restrict itself to that shape, and the build side is chosen by size:
`buildLeft = lRows > 0 && rRows > 0 && lRows < rRows` (`internal/planner/bushy.go:1382`), with a
small-dimension override immediately after (`:1378-1386`). The codebase states the consequence
itself, in `parallel_hash_build.go:163-166`: *"The walk descends the PROBE side only. A hash join
nested on another join's build side is built as part of that build."*

Q5 — the query with the worst regression in the whole evidence set — is the concrete case
(`plan_snapshots/r5-default.txt:62-71`):

```
->  Hash Join (INNER)  rows=402301450
  ->  Seq Scan on public.lineitem  rows=5999786
  ->  Hash Join (INNER)  rows=1991195     <- the sub-join is the BUILD side (1.99M < 6.0M)
```

The deepest and widest level has its sub-join on the **build** side — correctly, because it is
the smaller side, which is what PG would also do. Slot chaining does nothing for that level. The
two levels above it do have their sub-join on the probe side and do benefit.

Two consequences that must not be glossed:

1. **Stage 0's arithmetic counts probe-side seams, not levels.** A per-level estimate overstates
   the win.
2. **Plan-shape parity with PG is not achieved by removing MHJ alone.** A bushy goopg tree is not
   PG-isomorphic to a left-deep PG tree even after the MHJ node is gone. Stage 4's case must
   answer "does the DP produce a left-deep, fact-outermost shape for these queries at all?" —
   and today the answer is no. Combined with the missing `Hash` node
   ([06 §4](06-explain-and-plan-shape.md)), the plan-shape benefit is **two steps further away
   than the proposal assumes.**

## 11. Five concrete defects that block reusing `multi_hash_join.go`

§6 rules the "closer to a relocation" claim half true on coordinate-derivation grounds. The
second pass enumerated five independent blockers, each verified:

| # | defect | evidence | why it blocks reuse |
| --- | --- | --- | --- |
| 1 | **Unbounded build: no `WorkMem`, no spill.** MHJ drains each build child with `drainRowsCtx` (`multi_hash_join.go:184`) into an unbounded `[]Row`, then builds a map on top, holding both. `ctx.WorkMem` appears nowhere in the file (it is used at `operators_join_agg.go:485,531`). | grep | A fused operator inheriting this **loses `joinOp`'s spill** (`drainRowsBounded`, `spill.go:342`) — a memory regression dressed as a performance win, in the exact dimension this work exists to fix. Contract [03 C8](03-semantic-contract.md). |
| 2 | **The spanning-tree walk silently drops join edges.** `multi_hash_join.go:130-165`: a greedy source-order walk; `if buildTbl == probe \|\| visited[buildTbl] { continue }` skips a cycle-closing edge, and `if !found { break }` (`:162`) exits with `len(keySteps) < len(Keys)`. The surplus equijoins are **not** re-homed as residual filters. | code | A dropped equijoin ⇒ **over-emission** ⇒ a silent row-count regression. Tracked as [08 R1](08-risk-register.md). |
| 3 | **Unreached tables get an arbitrary hash key.** `keyColByTable[i] == -1` falls into the fallback at `:190-205`, hashing on the first key column mentioning the table, which contributes no matches. | code | Silent row **loss**. |
| 4 | **A whole re-coordinatisation layer.** `rewriteMultiWayChain` sorts `Tables` by catalog OID (`bushy.go:1790-1830`), then `buildMHJPosMap` (`:2349`) / `remapColumnRefsAfterRewrite` (`:1854`) rewrite `ColumnRef.Index` across the rest of the tree. Note the OID sort is **not itself the bug** — its own comment (`bushy.go:1790-1795`) says it exists *to match* the replaced binary tree's FROM-order schema; the skew it repairs comes from the bushy DP's DFS walk order. The hazard is the remap layer the flattening makes necessary. | code | A runtime fusion **must not need a remap layer at all** ([04 §4](04-fusion-site-and-data-structures.md)). Reusing MHJ's composition means reusing the flatten-then-remap round trip, which is the thing we are trying to delete. |
| 5 | **Incomplete residual-filter walker.** `walkColumnRefs` (`multi_hash_join.go:379-408`) has no arm for many `Expr` kinds and falls through silently; an unhandled filter is classified probe-only and evaluated **too early**. | code + `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md` | Wrong-*time* residual evaluation is a correctness bug on LEFT/anti shapes and a performance bug on INNER. Contract [03 C4](03-semantic-contract.md) forbids inheriting `partitionFilters` for exactly this reason. |

What *is* reusable is the **idea** and the **shape of the qualification predicate** (doc 15 §2).
Everything else is a rewrite against `joinOp`'s semantics. Budget accordingly — and note that
[05 Q8](05-qualification-predicate.md) shows most of that predicate becomes unnecessary anyway,
because the plan has already fixed the shape.

## 12. The benefit the proposal does not claim, which is the best one

Deleting `MultiHashJoin` as a plan node deletes a large, actively-bleeding surface in the
**planner**, not the executor:

- `rewriteMultiWayChain`, `collectMultiHashTables`, `rewriteMHJInputsWithSingleTablePredicates`,
  `buildMHJPosMap`, `remapColumnRefsAfterRewrite` / `remapPosMapAfterRewrite`, and
  `attachUnusedCrossEdges`'s MHJ-only global-coordinate path (`bushy.go:1416` comment) —
  hundreds of lines whose entire job is to repair index skew introduced by flattening a bushy DP
  tree into an OID-ordered table list and remapping every `ColumnRef` back.
- **28 non-test `case *MultiHashJoin:` arms across 15 files** (verified by grep): `unnest.go` ×7,
  `bushy.go` ×4, `mhj_input_rewrite.go` ×3, `parallel.go` ×2, `operators_explain.go` ×2, and one
  each in `executor.go`, `subplan.go`, `subplan_lower.go`, `subplan_lower_walk.go`,
  `subplan_cost.go`, `pushdown.go`, `inner_join_qual_pushdown.go`, `nl_index_join.go`,
  `exists_to_any.go`, `view_privilege.go` — each an independent chance for a new pass to forget
  the node.
- The bug class is live, not historical: `exists_to_any.go:476` warns that "MultiHashJoin OID
  re-sort or bindings remap can leave it stale", and the tip-of-branch commit `23a077ae` is
  another instance.

**That is the honest business case for this bundle: not speed, but the permanent removal of an
index-skew bug generator. Speed must merely not regress.**
