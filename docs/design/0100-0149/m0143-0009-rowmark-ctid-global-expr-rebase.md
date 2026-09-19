# M0143-0009 — `FOR UPDATE` over joins: global expression rebase after resjunk-ctid injection

Status: implemented (this loop).
Task: `.ralph/fix_plan.md` M0143-0009. Resolves the residual of
`.ralph/deferral_ledger.md` row `AI-007 self-join lock` (2026-08-09).

## Bug

`wireRowMarkCtidColumns` (internal/optimizer/planner.go) appends a resjunk
`ctid<N>` column to every rowmarked leaf scan so `LockRows` can capture heap
TIDs without the side-channel fallback. Appending to a leaf that sits on the
**left** of a join shifts every later sibling's `ColumnRef.Index` in the
merged row — but `recomputeIntermediateSchemas` rebased only `*Project`
targets (`fixColumnRefIndices`, name-keyed). Every other expression holding
absolute child-row positions — `Join.Predicate`, hash/merge keys,
`Filter.Predicate`, `Sort`/`Aggregate`/`DistinctOn` keys, NLI residuals and
outer-scoped probe keys — kept stale indices.

Live repro at HEAD `2b7e97053` (scratch cluster, NL and Hash both):

```sql
SELECT a.i FROM a JOIN b ON a.i=b.i WHERE a.i=31;          -- 1 row
SELECT a.i FROM a JOIN b ON a.i=b.i WHERE a.i=31 FOR UPDATE OF a;  -- 0 rows (WRONG)
... FOR UPDATE;                                           -- 0 rows (WRONG)
... FOR UPDATE OF b;                                      -- 1 row (rightmost leaf: append at end, no shift)
```

`EXPLAIN ANALYZE` showed `Rows Removed by Join Filter: 1` — the merged-row
predicate reads the injected ctid datum instead of `b.i`. The
`hasSelfJoinLockedTable` guard (AI-007 workaround) masked only the
same-OID-double-scan case; any non-rightmost locked leaf was corrupted.

A second latent defect on the same mechanism: `Distinct.schema`,
`DistinctOn.schema`, `OrdinalityWrap.schema`, `ProjectSet.schema`,
`Gather.schema`/`GatherMerge.schema` are cached copies that went stale after
injection, and `distinctOp` dedups `rowKey(wholeRow)` — a unique ctid datum
mid-row makes every row distinct.

## Design

PostgreSQL never has this problem because resjunk ctids are added to the scan
targetlist at path-construction time and `setrefs.c` remaps every Var through
the built targetlists. goopg's post-hoc injection needs the same *global*
rebase. Three coordinated changes, all in `internal/optimizer/planner.go`
plus a small schema flag and two executor touch-ups:

### 1. `SchemaColumn.Resjunk`

New field on `SchemaColumn` — the faithful analogue of PG's `resjunk` tlist
mark. `tagScan` sets it on injected `ctid<N>` columns. It rides along with
schema appends (whole-struct copies) and drives:

- `LockRows.Output()` / the executor's emit-time strip: drop positions whose
  child-schema column is resjunk, instead of assuming the ctids are the last
  `NumCtidCols` positions (a left-leaf injection puts them mid-row).
- `distinctOp` dedup key: exclude resjunk positions (PG dedups on the
  distinct clause columns; the resjunk datum still physically rides the
  emitted tuple up to LockRows, exactly as in PG).

### 2. Positional rebase walk — `rebaseRowMarkPlan`

`wireRowMarkCtidColumns` is split: tagging leaves now also snapshots
`oldW[n] = len(n.Output())` for every visited node *before* any append
(top-of-walk, so pass-through `Output()`s still read pre-injection child
schemas). The root-Project target append moves out to a final step.

The new post-order walk returns, for each node, a `remap []int` of length
`oldW[n]` mapping old output position → new output position, rebasing every
expression in the node's own coordinate space:

- **Leaves**: identity over `oldW` (appends only grow the tail).
- **`*Join`**: schema rebuild (existing behaviour); `Predicate`,
  `LeftKey`/`RightKey`, every `HashKeys` pair, and `UsingLeftCols`/
  `UsingRightCols` are all **merged-coordinate** — the executor evaluates
  hash and merge keys through `mergedKeySlot`/`keySlot.rebind` VirtualSlot
  views over the `[left ++ right]` row (operators_join_agg.go,
  join_merge_stream.go), not side-local rows. The merged remap is
  `leftRemap ++ (newLeftW + rightRemap[j])` — an old right-side position j
  (merged index len(left)+j) lands at the right child's *new* local
  position plus the new left width. Semi/anti joins return `leftRemap`
  (they emit only the outer side) but still rebase predicate/keys over
  `merged` since the executor evaluates those against the padded row.
- **`*NestedLoopIndexJoin`**: `Predicate` over merged; the inner probe's
  `Key`/`Keys`/`SAOPKeys`/`LowKey`/`HighKey`/`Pred` are **outer-scoped** —
  rebased over outerRemap (recursing into `BitmapHeapScan.Outer`'s bitmap
  subtree). Inner-local `Cond`/`BitmapQual` keep leaf-local coordinates
  (identity — leaf appends don't move their own positions).
- **Unary pass-throughs** (`Filter`, `Sort`, `Limit`, `Memoize`, `Gather`,
  `GatherMerge`): exprs rebased over childRemap; cached schemas (`Gather`,
  `GatherMerge`) rebuilt from the child.
- **`Distinct`/`DistinctOn`**: cached schema rebuilt from child;
  `DistinctOn.KeyCols` remapped positionally.
- **`OrdinalityWrap`/`ProjectSet`**: schema = new child ++ old suffix;
  `ChildWidth`/`EvalRowWidth` shifted by the child's growth; SRF arg/result
  exprs rebased over childRemap.
- **`Aggregate`/`WindowAgg`**: group/passthrough/agg-arg/order/filter exprs
  over childRemap; `InputTarget` int lists remapped; output schema is the
  node's own → returns identity over `oldW`.
- **`Project`**: `Targets` rebased over childRemap positionally (retires the
  name-keyed `fixColumnRefIndices`/`fixColumnRefsInExpr`, which are deleted —
  positional is collision-free where (name,srcIdx) is not); output = its own
  target schema → identity over `oldW`.
- **`Result`**: `Targets`/scalar exprs over childRemap when `Child != nil`.
- DML wrappers / `CTEDMLPrefix` / `RecursiveUnion` / `SetOp`: children walked;
  unreachable in practice under rowmarks but kept symmetric so a future
  injection point can't silently corrupt them.
- Column refs whose `Index` is out of the old-width range pass through
  unchanged (defensive; no legitimate ref exceeds it).

Refs inside scope-opening exprs (`SubqueryExpr` etc.) are skipped by
`exprChildSlots` slot kinds, same as the old helper.

A walk-wide `seen map[*ColumnRef]bool` dedupes rebased refs: plan fields
alias each other — `HashKeys[0]` is `{LeftKey, RightKey}` **by pointer**
(`fillOneJoinHashKeys`), non-ColumnRef key exprs share their inner refs
with `Predicate` (`cloneKeyExpr` clones only bare ColumnRefs), and a
pushed-down conjunct can share refs between a join predicate and an
ancestor filter. In-place rebasing without dedupe applies the map twice
to a shared ref (the exact corruption class M0097-0060 documented for
`reresolveJoinByName`).

### 3. Ctid resno resolution for every root shape

`CtidResno` was set only when the root was `*Project`; join-rooted plans
(`SELECT *`) injected ctids nothing could locate. After the rebase, a new
`resolveRowMarkCtidResnos` step appends the ctid targets to a root `*Project`
(Index resolved by name against the child schema — the `ctid<N>` names are
unique by construction) and then sets every wired `LockedRel.CtidResno` to
its column's position in `root.Output()`. Unwired locks keep `-1` (fallback
to the executor's side-channel path unchanged).

The same step maintains two EPQ-merge coordinates on each `LockedRel`:

- `ColOffset` (binding's first column in the FROM-order merged row) is
  shifted past earlier ctid insertions: a ctid whose *final* position is f
  was inserted at old-coordinate position f − (earlier insertions), so the
  sorted final positions recover the insertion points exactly.
- `ColPos []int` (new) is the exact position of each locked-rel column in
  the LockRows child's output row, resolved by `(Name, SourceTableIdx)`
  identity against the leaf schema carrying that lock's `ctid<N>` column.
  `ColOffset+i` only coincides with it when the child row is the
  FROM-order merged row — a top Project may subset or reorder. Columns the
  projection doesn't carry get `-1` (nothing to merge into). The
  executor's EPQ refetch-merge prefers `ColPos` when populated.

### 4. `hasSelfJoinLockedTable` retired

The AI-007 guard existed solely to avoid the rebase hole. With the global
rebase, self-join `FOR UPDATE` locks via the real ctid path like every other
shape: each RTE's scan gets its own `ctid<rowmarkId>` column
(`nextLockIdx` already assigns per-binding rowmarks), the merged row carries
both, and positional strip removes them. The locking_test expectations that
pinned `NumCtidCols == 0` for self-joins are updated to pin the new
behaviour instead.

## Executor changes

- `lockRowsOp` emit paths (`Next` tail-trim and the `merged[:len(schema)]`
  trim): strip by resjunk-position set derived from `o.plan.Child.Output()`,
  falling back to trailing `NumCtidCols` when no resjunk marks exist
  (hand-built plans in tests).
- `distinctOp`: build a resjunk skip-set from `p.Output()` and key dedup on
  non-resjunk positions only.
- No change to `drainAndStamp`/`stampLock`/`epqRecheckFilter`: they already
  read `row[lk.CtidResno]`, which now resolves correctly for every root
  shape.

## Tests

`internal/executor/operators_lockrows_test.go` gains a two-table fixture
and `TestLockRowsJoinCtidShift` arms: `of_left_leaf`, `bare_for_update`,
`of_right_leaf`, `of_left_swapped_from` (locked side rightmost),
`star_join_rooted` (`SELECT *`, exercises non-Project strip),
`order_by_above_join`, `distinct_star`, `self_join_bare` — each asserting
the locked result equals the unlocked result. All failing arms verified
red pre-fix (0 rows for left-leaf/bare/join-rooted, wrong values for
right-leaf, wrong dedup for DISTINCT). `internal/optimizer/locking_test.go`
self-join pins updated for the retired guard.

Also verified end-to-end on a scratch cluster: `FOR UPDATE OF` left/right/
bare over NL+IndexScan and SeqScan+HashJoin shapes (5000-row equijoin,
all arms returning the full match set), `SELECT *` join-rooted,
`DISTINCT`, and a self-join.

## Non-goals / residual

- SetOp/RecursiveUnion dedup rows can't carry rowmark ctids (PG rejects
  rowmarks on set-op children); left as-is.
- A `FOR UPDATE` leaf under `Gather` is unreachable (`StripGather` post-pass
  already removes search-chosen Gathers under LockRows).

## Update 2026-09-20 — surface ctids through schema-pinning ancestors (AI-20260920-005626-006)

**Defect.** `resolveRowMarkCtidResnos` resolved each wired ctid's resno by
name lookup in `proj.Child.Output()` — correct only while the node below the
top Project derives its schema live from the leaves. The planner also emits a
column-reordering Project between the top Project and the join (restrip to
binding order) — e.g. `SELECT a.accountid, a.balance FROM lrs_acct a,
lrs_side s WHERE a.accountid = s.k FOR UPDATE OF a` when the hash outer is
`s`. That intermediate Project pins its own schema (`Project.Output()`
returns `p.schema`, not `Child.Output()`), so the leaf-injected ctid never
appeared in the resno lookup: `CtidResno` stayed -1, yet `NumCtidCols`
still counted the leaf injection, and `LockRows.Output()`'s trailing-strip
fallback then dropped a real user column per lock (`balance`; both output
columns for a bare two-table `FOR UPDATE`).

**Fix.** New `surfaceRowMarkCtid` walks the tree post-order and threads each
wired ctid up through schema-pinning ancestors before the resno is read:

- `*Project`: append a pass-through `ColumnRef` + `Resjunk`-marked schema
  entry when the child output carries the ctid but the Project's schema
  does not (idempotent on re-entry).
- `*Join` / `*NestedLoopIndexJoin`: rebuild `schema` via `appendSchema`
  after children are threaded (skipped for Semi/Anti — left output only).
- `*Distinct` / `*DistinctOn`: `schema = Child.Output()`.
- `*Aggregate` / `*WindowAgg` / `*OrdinalityWrap` / `*ProjectSet` /
  `*SetOp`: NOT recursed — their pinned schemas interleave or synthesize
  columns (or combine two branches), so threading a ctid underneath would
  corrupt their layout, and PG cannot take rowmarks across these
  boundaries anyway. A ctid buried there stays unresolved → `CtidResno=-1`
  → executor falls back to the walker/side-channel paths.
- Filter/Sort/Limit/Memoize derive `Output()` from the child live, so no
  rebuild is needed.

`NumCtidCols` is then recounted at the call site to only the locks whose
`CtidResno` resolved — the trailing-strip fallback can no longer fire for a
ctid that never reached the LockRows child row.

**Regression coverage.** `TestPlanCtidRowMarkDoubleProject`
(`internal/optimizer/locking_test.go`) constructs the
LockRows→Project→Project→Join shape by hand and pins `CtidResno ≥ 0`, the
mid-Project's threaded ctid, and the 2-column `LockRows.Output()`. The
original witness, `TestPort_LockRowsSortOverJoinTakesRowLock`, passes at
the fix (5.39s) — and now exercises the resjunk-column path end to end
(the lock blocks, wakes on writer commit, EPQ returns the updated row).
