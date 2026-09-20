# M0141-S2a-fix — scoping recon: which half is cheaper, and what PG quantity to substitute

Status: accepted (landed 2026-09-15); parent task M0141-S2a-fix CLOSED
2026-09-20 — both halves delivered (half 1 by M0141-S2a-fix1, half 2 by
M0141-S2a-fix2 / M0141-S2a-fix2r), so the parent row carried a stale `[ ]`.

## Task

`.ralph/fix_plan.md` M0141-S2a-fix: "make `costAgg`'s width currency see the
post-narrowing input... Two coupled changes, not a one-line patch: (1) move
narrowing earlier or compute a narrowing preview at cost time... (2) pair it
with a corrected `inAvgVarBytes` currency... Needs a dedicated scoping pass
before attempting — size which half is cheaper first." Per the plan-parity
harness this is a scoping task; **no production code is touched by this
task.**

## Finding 1 — half (1) is not "planner-search-order surgery"; the preview already exists and is already live before costing runs

M0141-S2a's own resume note floated two equivalent framings for half (1):
"move (or duplicate, read-only, for costing purposes) narrowing *before*
`createGroupingPaths` runs — or equivalently, make `aggInputWidth` compute
what the input width *would be* after narrowing, at cost time." Tracing the
call graph shows the second framing is not a future thing to build — it is
**already sitting on the node, unused**:

- `internal/optimizer/planner.go:1738` calls `buildAggregateStage`, which
  (at `planner.go:8219`, inside `buildAggregateStage`) calls
  `stampAggregateInputTarget(aggNode, nil)` — this is **before**
  `createGroupingPaths` runs at `planner.go:1760`.
- `stampAggregateInputTarget` (`group_input_target.go:267`) →
  `deriveAggregateInputKeep` (`:190`) computes, by NAME (group exprs ++ agg
  args/filter/order-by ++ passthrough — the Aggregate's own complete read
  set, per `upper_narrow_apply.go`'s "the Aggregate IS the absorption
  point" comment), the exact keep-list `agg.InputTarget []int` (index
  positions into `agg.Child.Output()`) that the REAL rewrite
  (`narrowAggregateInput`, `upper_narrow_apply.go:210`) later re-derives,
  re-verifies, and commits — but only at `Plan()`'s tail (`:189`), long after
  costing.
- `addGroupingPaths` (`groupingpaths.go:340`), called from
  `createGroupingPaths` (`:92`), already receives `aggNode *Aggregate` as a
  parameter — so `aggNode.InputTarget`/`InputTargetKnown` are reachable at
  the exact point `aggInputWidth(child)` is called (`:344`) without adding a
  single new parameter, without touching `Plan()`'s call order, and without
  moving `applyUpperNarrowing` at all.

So the "move narrowing earlier" question is moot: the **keep-list** (which
columns the aggregate needs) is already computed early enough. What is
missing is a three-line change — restrict the columns `aggInputWidth`/
`nodeAvgVarBytes` (`upperrel.go:205`) sum over to `agg.InputTarget`'s
indices when `InputTargetKnown`, instead of the raw `child.Output()`. This is
bounded to `groupingpaths.go` (and its two siblings that call the same
helper — see Finding 3) and needs no pipeline reordering. **Half (1) is the
cheap half**, contrary to the "planner-search-order surgery" framing S2a's
own resume note used.

Why S2a's own trace didn't find this: its Finding 2 traced only the JOIN-LEG
narrowing mechanism (`narrowJoinLeg`/`GOOPG_NARROW_LEG_HOOK`) and the FULL
rewrite commit (`applyUpperNarrowing`) — both real production narrowing
passes, but neither is a "preview". `agg.InputTarget` is a third,
already-existing artefact: a cheap, gate-free, NAME-derived keep-list
computed specifically so a later pass (originally: the B-01c apply pass;
here: costing) doesn't have to re-derive it. S2a's Finding 2 did not name it
because its subject was "why did the *rewrite* not fire", not "is a
preview available".

## Finding 2 — the PG-equivalent quantity to substitute is `subpath->pathtarget->width`, and it is exactly this: the query-wide column-need computation, done early

B2 requires deriving the substituted quantity from PG source before any
parity number is taken. PG's `cost_agg` (`costsize.c:2682`) takes
`input_width` as a plain parameter; its one production caller,
`create_agg_path` (`postgres/src/backend/optimizer/util/pathnode.c:3430`),
passes `subpath->pathtarget->width` — the width of the **already-built**
`PathTarget` of the path feeding the aggregate. The executor side confirms
the same currency: `hash_agg_entry_size` (`nodeAgg.c:1701`) is called at
`nodeAgg.c:3701-3703` with `outerplan->plan_width` — the child PLAN node's
own (already narrow) width, not a separately derived "group key width".
Both the memory-fit decision (`hash_agg_entry_size`, ExecInitAgg) and the
planning-time cost estimate (`cost_agg`) key on **one and the same
quantity: the width of whatever the aggregate's input node actually,
finally, emits** — and in PG that is narrow from the moment the RelOptInfo's
`reltarget`/`pathtarget` is built (`build_joinrel_tlist` et al., driven by
query-wide `attr_needed` analysis computed once, ahead of any path costing —
`initsplan.c`), not a late add-on.

goopg has no equivalent "compute every rel's needed columns once, up front,
before any path is costed" architecture (that is the K24/M0141-S2b item, the
workstream's largest single piece, explicitly out of scope here). The
absorption-principle substitute is: use `agg.InputTarget`'s NAME-derived
keep-list (Finding 1) as goopg's own version of that same computation — it
answers exactly the question `build_joinrel_tlist` answers for the
Aggregate's own boundary ("which columns does the query still need from
this point"), just computed later and per-node instead of once and
query-wide. This is the derive-before-measure requirement satisfied: the
substituted quantity is not a fitted constant, it is goopg's existing
name-derived equivalent of PG's `pathtarget`.

Citations: `postgres/src/backend/optimizer/util/pathnode.c:3430-3434`
(`cost_agg` call site, `input_width = subpath->pathtarget->width`);
`postgres/src/backend/optimizer/path/costsize.c:2793-2802`
(`hash_agg_entry_size(..., input_width, ...)` and
`relation_byte_size(input_tuples, input_width)`, the same `input_width` used
for both the entry-size/nbatches decision and the spill-page estimate);
`postgres/src/backend/executor/nodeAgg.c:1701-1730` (`hash_agg_entry_size`
definition) and `:3701-3703` (`outerplan->plan_width` as the executor-side
value of the same currency).

## Finding 3 — half (2), the currency shape (`hashAggEntrySize`), is a SEPARATE, smaller question and should be attempted only after half (1) is measured

`cost_funcs.go:516-540`'s spill arm currently charges only the VARIABLE
payload (`inAvgVarBytes`) — `hashAggEntrySize` does not add the fixed
per-column overhead (`48*ncols+24`, `hashsize.EntryBytes`'s shape) the
SORTED rival's currency (`costSortRunWithWidth` via
`hashsize.EntryBytes`) does. R120 built a corrected currency behind
`GOOPG_HASHAGG_WIDTH_CURRENCY`; R124 §7 paired it with an (earlier, pre-M0139,
not proven free of the same pipeline-ordering defect S2a's Finding 2 found)
narrowing pass and measured it identical to the currency-alone case;
M0137-0009 deleted the flag. Nothing here reopens that verdict — R124's
narrowing partner is not the same artefact as Finding 1's `agg.InputTarget`
preview and was never live at cost time either way, so "narrow, then
re-measure" has genuinely never been tried with a preview that is actually
present when `costAgg` runs. But per the task's own text, this is the
higher-risk half: it changes an already-tuned, already-measured formula
(`hashAggEntrySize`), not just its input.

**Sizing verdict**: attempt half (1) first, alone, unpaired with a currency
change, and measure. Reasons:
- It is a three-line, single-file change (Finding 1) with a source-grounded
  substitution (Finding 2) — the cheapest possible next slice.
- It is exactly what mechanism (A) (Q3) needs per S2's own diagnosis
  ("un-narrowed Hash Join output inflates `costAgg`'s R3 spill-arm width
  term, over-charging HASHED") — the currency shape is orthogonal to that:
  even the current under-stated currency (`inAvgVarBytes` alone), fed a
  correctly NARROWED `inAvgVarBytes` instead of the raw ~25-column
  `child.Output()` width, should shrink Q3's spill charge materially (S2a's
  live `EXPLAIN VERBOSE` capture: the Hash Join narrows to 7 of ~25 raw
  columns post-M0139, a fact the cost model currently never sees at all).
- R124 §7 already falsified "currency alone, no live narrowing" once; there
  is no reason to believe "currency + narrowing" fares differently until
  "narrowing, no currency change" has itself been measured in isolation —
  otherwise a regression from the currency half cannot be distinguished
  from a gain from the narrowing half.
- The three call sites that share `aggInputWidth` (`groupingpaths.go:344`,
  `partialaggpaths.go:338`, `partialaggupper.go:327`) all key off the same
  `Aggregate` node and the same `agg.InputTarget` field, so half (1)'s
  change composes for free across all three — it is not a Hashed-vs-Sorted-
  only fix, it also narrows the currency `costSortRunWithWidth` (the SORTED
  rival) already uses correctly, tightening both sides of the contest at
  once rather than one.

Half (2) (the entry-size currency correction) stays filed, gated on half
(1)'s own measured result, exactly as R124's history recommends: change one
variable at a time against a cost model this sensitive.

## Correctness note carried forward (not a blocker, recorded for the implementer)

Feeding `costAgg` a narrowed-preview width for the HASHED candidate is safe
under B2 even though goopg's `narrowAggregateInput` commit later DECLINES to
apply that narrowing for real when HASHED wins (`upper_narrow_apply.go:267`'s
`pastSort` retention-site condition — a hash aggregate retains transition
states, not rows, so there is no Sort to sink a narrowing `Project` below).
This is not a costing/execution mismatch bug: PG's own `hash_agg_entry_size`
(Finding 2) charges entry size from the input WIDTH regardless of whether
that width is "retained" in any row-storage sense — it is modelling "does
the table of `(group key, transition state)` entries fit", not "is the raw
input row kept around". B2's worked example says this explicitly for the
sibling hash-join case: "goopg may still really spill and run slower... a
slower plan that matches is not a regression. Plan parity does not require
goopg's memory footprint to equal PG's." The same reasoning applies here
without modification — record it so half (1)'s implementer does not
mistake the declined commit for a reason to gate the preview on `AggStrategy`.

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement**: none — recon only, no production code touched.
- **shape-delta**: 0.
- **Stats epoch**: not applicable (no live capture run by this task).
- **Seam-decline census**: not applicable.
- **Planning route**: newly established this task — `agg.InputTarget` is
  live and populated at `addGroupingPaths`'s call site
  (`groupingpaths.go:344`), before any candidate is costed; this is the
  planning-route fact half (1) depends on.

## Verification

- `go build ./...` and `git diff --stat -- '*.go'` both confirm no
  production file was touched by this task (recon only).

## Resume points (filed as separate fix_plan slices, see below)

- **M0141-S2a-fix1** (new) — the cheap half: teach `aggInputWidth`'s callers
  to compute `(ncols, avgVarBytes)` from `agg.InputTarget`'s kept columns
  when `agg.InputTargetKnown`, falling back to the full `child.Output()`
  otherwise (unknown target = no narrowing derivable, same as today). Pin
  with a test asserting the two currencies (cost-preview width vs the
  executor's actual, possibly-undeclined-narrowing width) are deliberately
  separate per B2's "pinned by a test" rule. Re-measure Q3/Q13/Q18 (and the
  TPC-DS AGGSPLIT-serial-only 32-query set from M0141-S1, out of scope for
  TPC-DS's AGGSPLIT-touched 51 until M0140's floor) after.
- **M0141-S2a-fix2** (new, gated on M0141-S2a-fix1's measured result) — the
  entry-size currency correction (`hashAggEntrySize`'s fixed-overhead term),
  re-derived per B2 against `hash_agg_entry_size`'s
  `MAXALIGN(SizeofMinimalTupleHeader) + tupleWidth` shape (not a verbatim
  reinstatement of the deleted `GOOPG_HASHAGG_WIDTH_CURRENCY`). Attempt only
  after fix1 lands and is measured in isolation.
