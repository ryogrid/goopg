# 04 — SubPlan Execution Engine for Irreducible Cases

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| owns phases | S2 SubPlan execution floor (D4.1, D4.2, D4.4, D4.5) · S3 Hashed SubPlan (D4.3) |
| upstream oracle | `postgres/src/backend/executor/nodeSubplan.c`, `postgres/src/backend/optimizer/plan/subselect.c` |

## 1. Purpose

Even after every decorrelation extension in [chapter 03](03-planner-decorrelation-extensions.md)
lands, some subqueries stay SubPlans — by legality (correlation not expressible
as a join), by policy (D2.3 non-goals: OR-position, targetlist sublinks), or by
choice (non-correlated EXISTS deliberately kept as a cached SubPlan,
`internal/planner/unnest.go:1916`). PostgreSQL runs exactly the same irreducible
shapes fast; goopg runs them slowly. This chapter closes that half of the gap:
it makes the SubPlan path itself cheap, PG-style, independent of whether the
planner managed to unnest.

Honest framing of the stakes [measured-at-HEAD e4a43ba6]: the executor caches
added since 2026-05 (M0058-0001 constant-key cache, `CorrSubqOps`,
`CorrSubqHashMaps`) already prevent the historical catastrophes — Q22 runs in
1.80 s (≈31× PG) and Q4 in 7.41 s (≈39× PG) at SF1, not the 1452×/1156× of the
May-baseline ratios in the 260718 plan-compare §7. What remains for this
chapter is:

1. **The per-invocation floor.** Q4 still pays `Build`→`Open`→`Next`→`Close`
   of the inner operator tree per outer-row EXISTS evaluation. The invocation
   count depends on the executor's AND short-circuit order: ≈57 K if the date
   conjunct filters first, ≈1.5 M if not (≈130 µs vs ≈5 µs per invocation to
   account for the measured 7.41 s; both are estimates derived from the PG
   plan-compare actuals — S0's V6 counters pin the real number). PG pays a
   parameter store + `ExecReScan` on an already-built plan (0.19 s total).
   Removing the rebuild floor is worth roughly the whole remaining Q4 gap
   either way.
2. **The algorithmic cliff.** The two correlated fast paths (`CorrSubqOps`,
   `CorrSubqHashMaps`) exist **only** for scalar subqueries reached via
   `subqueryImpl`; the IN and EXISTS eval sites have neither. A correlated
   EXISTS that the planner fails to unnest is one plan-shape away from
   O(outer × inner) again.
3. **Fragile cache keying.** Correlated cache keys serialize the *full* outer
   row instead of the referenced correlation columns, so reuse collapses
   whenever any non-referenced column differs; and one eval site caches under
   a key that is not namespaced by subquery identity at all (§2).

## 2. Current state (inventory the design must not idealize)

The three eval sites in `internal/executor/expr.go` behave differently; the
table below is the ground truth this chapter's decisions modify.

| eval site | non-correlated | correlated cache | correlated fast path | per-miss cost |
| --- | --- | --- | --- | --- |
| `collectInValues` (expr.go:6674) — `IN (subq)` | constant key `nonCorrelatedCacheKey` (expr.go:6694-6697, :12089) | `SubqueryCache` keyed `subqueryCacheKey(row)` — full outer row, **no per-subquery namespace** | none | full `Build`→`Open`→drain→`Close` (expr.go:6711-6719) |
| `evalExistsExpr` (expr.go:6784) → `existsImpl` (:6827) — `[NOT] EXISTS` | constant-key cache (expr.go:6797-6822) | **none** — correlated EXISTS goes straight to `existsImpl` every row (expr.go:6824) | none (only the `lockRowsOp` maxDrain=1 first-row cutoff, expr.go:6832-6836) | full Build/Open/Next/Close per outer row |
| `evalSubquery` (expr.go:6860) → `subqueryImpl` (:6912) — scalar | constant-key cache | keyed `fmt.Sprintf("%p\|%s", x, subqueryCacheKey(row))` (expr.go:6885) — pointer-namespaced but still full-row | `CorrSubqOps` pre-opened rescannable op for `planIsIndexScanBased` shapes (expr.go:6924-6944, :6992); `CorrSubqHashMaps` whole-inner-table map for `Project(Filter(SeqScan, col = OuterColumnRef))` (expr.go:6947-6973, :7020) | full Build/Open/Close fallback (expr.go:6976-6985) |

Supporting state on `Context` (`internal/executor/context.go`):

- `OuterRows []Row` (context.go:91-97) — lexical scope stack; `OuterColumnRef`
  is resolved at eval time as `OuterRows[len-Level]` (expr.go:372 dispatch).
- `SubqueryCache map[string][]Datum` + `SubqueryCacheScope` (context.go:99-107)
  — cleared whenever the outer-scope depth changes. The comment at
  context.go:104-105 itself concedes the cache is only "notionally
  per-subquery"; for correlated `collectInValues` the key is the bare outer-row
  serialization, so **two distinct correlated IN sites at the same outer depth
  with equal outer rows collide**. This is a latent wrong-results hazard, not
  just a performance wart, and S2 removes it structurally (D4.4).
- `CorrSubqOps map[*planner.SubqueryExpr]Operator` (context.go:109-115) —
  pre-built, pre-opened operators; `indexScanOp.Open` detects re-`Open` and
  skips `openPrep` (lock acquire + btree open). Never explicitly closed.
- `CorrSubqHashMaps map[*planner.SubqueryExpr]map[string]Datum`
  (context.go:117-123).

Additionally, subquery expressions are opaque to `MultiHashJoin`'s filter
placement (`internal/executor/multi_hash_join.go:386`), so un-unnested
subquery conjuncts land in `leafFilters` and re-enter these eval sites from
the innermost step (§8).

**Scope note — the three sites above are not the whole `OuterRows` surface.**
Further Build-per-row eval sites exist for less common shapes: correlated
row-comparison subqueries (expr.go:6521, :6609), `evalArraySubquery`
(expr.go:7135-7141), and `evalMultiAssignSubqRow` (expr.go:7191). D4.1's
lowering covers `SubqueryExpr`/`InExpr`/`ExistsExpr` in S2;
ArraySubquery and row-comparison subqueries are **explicitly deferred to an
S3 follow-up** (they keep the stack path until then). Separately, the
NL-join fallback pushes its left row as an outer scope per iteration
(`internal/executor/operators_join_agg.go:270`) — that is a *join* scope,
not a SubPlan scope, and remains a permanent `ctx.OuterRows` user.

## 3. D4.1 — PARAM_EXEC analog: parameter slots instead of row-stack walks

### 3.1 PG contract

PG never re-plans or re-instantiates a SubPlan per outer row. At plan time,
each correlated Var in the subquery is replaced by a `PARAM_EXEC` Param
(`SS_replace_correlation_vars`, `postgres/src/backend/optimizer/plan/subselect.c:1971`);
the SubPlan node (`make_subplan`, subselect.c:162) carries `parParam` (the
param IDs the outer query must supply) and `args` (the outer expressions that
produce them). At execution, the *enclosing expression's* compiled steps
evaluate `args` into the `ParamExecData` slots (`ExecInitSubPlanExpr` emits an
`EEOP_PARAM_SET` step per arg, `postgres/src/backend/executor/execExpr.c:2821`,
:2841-2855); `ExecScanSubPlan`
(`postgres/src/backend/executor/nodeSubplan.c:204`) then marks **every**
`parParam` dirty in the child's `chgParam` set — unconditionally, PG performs
no value comparison ("record the fact that the values *might have* changed",
nodeSubplan.c:236-244) — and rescans (nodeSubplan.c:244).

### 3.2 goopg design

Introduce per-query parameter slots:

```go
// Context (internal/executor/context.go)
ParamExec []Datum      // PARAM_EXEC analog: one slot per plan-assigned param ID
ParamDirty bitset      // params written since the subplan's last rescan (chgParam analog)
```

Planner side — a lowering step run on every SubPlan that survives planning
(the goopg analog of `SS_replace_correlation_vars`):

- Walk the inner `Plan` of each retained `SubqueryExpr`/`InExpr`/`ExistsExpr`;
  for every distinct `OuterColumnRef{Level, Index, SourceTableIdx}`
  (`internal/planner/plan.go:437`), allocate a param ID from a per-statement
  counter and rewrite the ref to a new `ExecParamRef{ID}` planner node. (A
  new node name is required: `planner.ParamRef` already exists as the
  extended-protocol *bind-parameter* placeholder, plan.go:457-461 — PG's
  PARAM_EXTERN. `ExecParamRef` is the PARAM_EXEC side of the same
  `paramkind` split PG makes.)
- The walk must be **depth-tracked**: `Level` is scope-relative
  (`OuterRows[len-Level]`, expr.go:372-381), so a naive flat walk
  misclassifies refs sitting inside *nested* subplans. A `Level ≥ 2` ref in
  an inner SubPlan is forwarded through the intermediate SubPlan's
  `ParParam`/`Args` chain: the intermediate subquery grows a param whose arg
  is itself an `ExecParamRef` (or, for scopes owned by the NL-join push —
  §2 scope note — a stack-resolved `OuterColumnRef`).
- Record on the subquery expr node: `ParParam []int` (slot IDs) and
  `Args []Expr` that fill them — plain `ColumnRef`s for immediate-parent
  refs, `ExecParamRef`s for forwarded deeper levels (so `Args` are **not**
  always plain `ColumnRef`s).
- `IsNonCorrelated` becomes derivable (`len(ParParam) == 0`) instead of a
  separately computed flag; keep the flag during migration, assert agreement.

Executor side — each eval site, per outer row: evaluate `Args`, write the
slots, set `ParamDirty` bits **only for slots whose value actually changed**
(datum comparison), then drive D4.2's rescan. The value comparison is a
deliberate **beyond-PG shortcut** — PG dirties every `parParam`
unconditionally (nodeSubplan.c:236-244) — and is legal only under the §4.2
cacheability gate (no volatile calls, no LockRows in the inner plan); for
non-cacheable subplans every param is marked dirty, PG-style.
`evalOuterColumnRef`'s stack walk (expr.go:372) remains for the non-SubPlan
users of `OuterRows` during migration; **SubPlan** inner plans stop consuming
`ctx.OuterRows` (the NL-join scope push and the deferred
ArraySubquery/row-compare shapes keep using it — §2 scope note).

**Multi-level correlation.** One flat slot space per statement, allocated
across all nesting levels (PG does the same — param IDs are global to the
PlannerInfo tree). An inner SubPlan that references `Level=2` gets a slot
filled by *its* outer SubPlan's eval site; because slot IDs are unique,
levels cannot collide and the `SubqueryCacheScope` depth-clearing heuristic
(context.go:107) becomes unnecessary for param-keyed caches.

**Why not keep `OuterRows`?** Three reasons: (a) O(1) fill of exactly the
referenced values vs pushing whole rows; (b) change detection — the row stack
cannot tell "same correlation values as last row", which D4.2/D4.3 need to
skip rescans and hash rebuilds (PG: nodeSubplan.c:118); (c) it makes D4.4's
projected key the natural key (§6) and removes the collision hazard of §2.

## 4. D4.2 — Rescan-not-rebuild operator contract

### 4.1 Recommendation: a narrow SubPlanHandle first

Two options were considered:

- **(a) Add `Rescan()` to the `Operator` interface.** Invasive: every operator
  in `internal/executor` must implement or inherit it; the PG analog is the
  full `ExecReScan` dispatch (`postgres/src/backend/executor/execAmi.c`, with
  `UpdateChangedParamSet` propagating `chgParam` to children, execAmi.c:96-117).
- **(b) A `SubPlanHandle` wrapper owned by the three eval sites.** Narrow:
  one struct, built lazily per subquery expr node (keyed by planner-node
  pointer, exactly as `CorrSubqOps` is today), holding the built operator
  tree plus the param metadata:

```go
type subPlanHandle struct {
    op        Operator
    parParam  []int      // slots this subplan reads
    hash      *subPlanHashTable // D4.3, nil unless hashed
    lastParams []Datum   // chgParam analog: rescan only when these differ
    reopenable bool      // plan shape supports cheap re-Open (see 4.2)
}
```

**Decision: (b).** Rationale: the eval sites are the only consumers today;
option (a) forces per-operator rescan semantics to be defined for ~40
operators up front, most of which never appear under a SubPlan. Option (b)
ships S2 with rescan support for the shapes that dominate (index scan, seq
scan, filter, project, aggregate, sort, limit, hash join) and leaves a clean
migration path: when S7's Memoize operator (chapter [05](05-memoize-operator.md))
needs a real rescan protocol for join inners, promote the wrapper's protocol
into the interface then, with the SubPlan sites as the proven reference
consumers. This mirrors PG history — `ExecReScan` predates Memoize. One
boundary to keep clear: the NLI join already has its **own** slot-based
inner-rescan seam (`openPrep` + `BindOuter`/`Rescan(slot, width)`,
`internal/executor/operators_nljoin.go:120, :239-241`); that seam is *not*
the `subPlanHandle` protocol, and Memoize integrates with it separately
(D5.3 — NLI inner contract change, chapter 05 §5). The two seams stay
distinct until an eventual interface promotion unifies them.

### 4.2 Re-open semantics per operator (inventory)

`CorrSubqOps` already proves the pattern for one family:
`planIsIndexScanBased` shapes (IndexScan, Project/Aggregate over IndexScan —
expr.go:6992-7005) support repeated `Open()` without `Close()`;
`indexScanOp.Open` detects reuse and skips `openPrep`. S2 extends the
inventory:

Because subquery inner plans run the **full** `planSelect` pipeline
(`planSelectWithParent` → `planSelect`, `internal/planner/planner.go:10247`,
including the rewrites at planner.go:945-966), a SubPlan root can be any
operator the pipeline emits — the inventory must cover MultiHashJoin, NLI and
LockRows too, not just the scan/aggregate shapes seen in TPC-H:

| operator family | today | S2 requirement |
| --- | --- | --- |
| `indexScanOp` | re-`Open` skips openPrep [verified] | re-position with new param-derived key on re-`Open`; no rebuild |
| `seqScanOp` | re-`Open` restarts the scan position (`curBlock=0`) but is **resource-unsafe**: each `Open` acquires a fresh `mctx` scratch context (`operators_storage.go:1257`) and possibly a new scan ring (:1261-1263) **without releasing the previous ones** (release happens only in `Close`, :1283-1308), re-records SSI SIREADs, and re-runs lock/privilege/NBlocks work | genuine new work, not just churn removal: a rewind path that reuses sctx/ring, skips re-lock, and does not duplicate SIREAD entries; params never invalidate a bare scan |
| Filter / Project | stateless pass-through | propagate re-`Open` to child |
| `limitOp` | **NOT stateless**: `skipped`/`emitted`/`inTiesPhase`/`tieKeyVals` are reset in neither `Open` (`operators.go:463-501` — it only re-evaluates the limit/offset exprs) nor `Close` (:504, child-only); `Next` gates on `emitted >= limitCount` (:513). Under handle reuse a retained `LIMIT 1` subplan returns EOF for every outer row after the first → NULL scalar results | reset the four fields at the top of `Open` (unconditionally correct today, prerequisite for any reuse) |
| Aggregate | `aggregateOp.Open` resets and rebuilds rows (per comment at expr.go:6999-7001) | acceptable — recompute is semantically required when a param changed; skip entirely when `ParamDirty ∩ parParam = ∅` |
| `sortOp` | re-`Open` is **corrupting, not rebuilding**: `Open` appends to `o.rows` without clearing (`operators.go:640-694`; only the spill-flush path truncates); `idx`/`spillFiles`/`mergeReady`/`ctidsDisabled` reset only in `Close` (:806-824). Re-Open without Close duplicates child rows, leaks spill files, and leaks leftover rows after a partial drain (the EXISTS case) | until an explicit reset/rescan path exists, the handle's rescan for Sort-rooted plans is `Close()` **then** `Open()` (correct today); the PG param-skip rule (`postgres/src/backend/executor/README:25-26`: Sort does not rescan its input if no parameters changed) arrives only with that explicit path |
| hash-join build side (`operators_join_agg.go`) | rebuilt per Open (≈) | rebuild only when `ParamDirty` intersects the params referenced **below the build side** — the `UpdateChangedParamSet` analog (`execAmi.c:107`; definition `execUtils.c:910`) |
| `multiHashJoinOp` | `Open` rebuilds **all** per-table hash tables every call (`multi_hash_join.go:94-101`) — correct but O(N·tables) per rescan | either the `ParamDirty` build-side skip, or classify MHJ-rooted subplans non-reopenable (Close+Open per dirty rescan) in S2 and optimize later |
| `NestedLoopIndexJoin` | re-`Open` re-runs `openPrep` unconditionally (`operators_nljoin.go:120`) | same treatment as MHJ; note its inner already has a private slot-based rescan seam (§4.1) |
| `lockRowsOp` | side-effecting: `drainAndStamp` stamps xmax row locks (`operators_lockrows.go:773`); reached from `existsImpl` via the `maxDrain=1` special case (expr.go:6832-6836) | **excluded from all result caching and the `lastParams` short-circuit** — see the cacheability gate below; rescan must re-execute so locks are stamped per outer row |

The `chgParam` analog is the load-bearing piece: with per-slot dirty bits from
D4.1, the handle compares `lastParams` against the current slot values and
(i) returns the previous result immediately when nothing changed (adjacent
outer rows sharing a correlation key — common after a sort), or (ii) re-opens
the tree, letting each level skip work whose param inputs are clean.

**Cacheability gate (required for the `lastParams` short-circuit and every
result cache in this chapter — a flagged beyond-PG divergence, D2.2).**
PG has *no value-based change detection anywhere*: `ExecScanSubPlan` dirties
every `parParam` unconditionally per outer tuple (nodeSubplan.c:236-244),
and PG's only param-keyed result caches are gated — hashed SubPlan is
uncorrelated-ANY-only (subselect.c:518-522) and Memoize rejects any volatile
function in the inner target or quals
(`postgres/src/backend/optimizer/path/joinpath.c:768-781`). goopg's
value-equality shortcut and D4.4's projected-key cache are therefore legal
**only when the subplan's inner plan is provably result-stable per param
tuple**:

1. no volatile function call anywhere in the inner plan (a subplan with
   `… AND random() < 0.5` re-executes per row in PG; freezing its result for
   repeated params is an observable divergence), and
2. no `LockRows` node — `EXISTS(SELECT … FOR UPDATE)` is live today via
   `existsImpl`'s `lockRowsOp` special case (expr.go:6832-6836), and caching
   it would silently skip xmax lock stamping (`drainAndStamp`,
   `operators_lockrows.go:773`).

The volatility/side-effect classifier (allowlist-based: stable/immutable
builtins pass, unknown → not cacheable) is **built in S2 as part of this
gate**; chapter 05's Memoize insertion rule (D5.1 step 5) then reuses it.
Non-cacheable subplans still get the handle's rebuild-avoidance (the operator
tree is retained), but every rescan marks all params dirty and re-executes,
exactly PG's contract. This gate is recorded as a fidelity-matrix row in
[chapter 02](02-pg-target-architecture.md) §7 and verified by matrix row M13
in [chapter 07](07-verification-and-measurement.md). Note today's
`SubqueryCache` (and `CorrSubqHashMaps`) already carry this hazard latently —
S2 fixes it rather than introducing it.

Rows produced by a prior rescan must not leak: the handle drains or discards
the operator's pending state before reuse (PG discards via the ReScan
protocol; the wrapper owns this explicitly).

### 4.3 What S2 deletes

- `existsImpl`'s per-row `Build` (expr.go:6828) and `collectInValues`'s
  per-miss `Build` (expr.go:6711) — both become handle lookups.
- The `CorrSubqOps` / `CorrSubqHashMaps` maps merge into the handle table;
  `subqueryImpl`'s three-path special-casing (expr.go:6912-6986) collapses to
  "get handle, rescan, read one". The `extractCorrSubqHashInfo` pattern-match
  (expr.go:7020) is subsumed: a seq-scan-shaped inner is simply a rescannable
  plan, and its O(N)-once behavior is D4.3's/S7's job to provide where
  profitable, chosen structurally rather than by executor pattern-matching.

## 5. D4.3 — Hashed SubPlan (S3)

### 5.1 PG contract

For an ANY SubPlan with **no** correlation (`parParam == NIL`) whose plan and
test expression are hashable, PG sets `useHashTable`
(`postgres/src/backend/optimizer/plan/subselect.c:518-522`) and the executor
loads the subquery output into an in-memory hash table once, probing per
outer row (`ExecHashSubPlan`, nodeSubplan.c:101; `buildSubPlanHash`,
nodeSubplan.c:477), rebuilding only if `chgParam != NULL` (nodeSubplan.c:118).
NULL semantics are preserved with a **second** hash table of partial-match
rows (rows with some NULL columns) so `NOT IN` three-valued logic stays
correct. Non-hashable uncorrelated subplans get a `Material` wrapper instead
(subselect.c:530-536).

### 5.2 goopg design

Applies to `InExpr` SubPlans with `len(ParParam) == 0` that were **not**
unnested (unnesting remains the first choice — chapter 03), and optionally to
non-correlated EXISTS (goopg extension: PG has no EXISTS hash because it
rewrites the test differently; for goopg's kept-as-SubPlan EXISTS the
first-row cutoff already bounds it, so hashing is only a minor win — decide
in S3 by measurement, default off).

- `subPlanHandle.hash`: `mainTable map[string]struct{}` over
  `datumKey(value)`, plus `hasNullRow bool` / `nullPartialTable` for the
  NOT-IN partial-match semantics, mirroring the two-table design of
  buildSubPlanHash. For goopg's current single-column IN test expressions the
  "partial match" degenerates to tracking whether any inner value was NULL;
  the design keeps the two-table shape so multi-column `(a,b) IN (...)`
  inherits correct semantics when it arrives.
- Probe result: `true` on match; `NULL` (not false) on miss when the inner
  set contained a NULL and the test is negated-or-null-sensitive — byte-for-
  byte the PG truth table, validated by the chapter [07](07-verification-and-measurement.md)
  NULL matrix.
- **Replaces, not stacks on,** the current path: today a non-correlated IN
  caches the full `[]Datum` under the constant key and then **linearly scans
  it per outer row** inside IN evaluation. S3 deletes that linear scan; the
  `[]Datum` materialization survives only as the non-hashable fallback (the
  `Material` analog — goopg's cached slice *is* its materialization, so no
  separate wrapper node is needed; document this as the deliberate divergence
  from subselect.c:530-536).
- Hashability gate: estimated inner size vs the D4.5 memory budget (PG's
  `subplan_is_hashable` tests against **hash_mem** =
  `get_hash_memory_limit()` = `work_mem × hash_mem_multiplier`,
  subselect.c:712-727 — goopg maps this to `Context.WorkMem`, see
  [chapter 06](06-cost-model-touchpoints.md) D6.4), using
  `EstimateRows` (`internal/planner/cardinality.go:38`); fall back to the
  slice when over budget.
- Cacheability: the §4.2 gate applies — a volatile or LockRows-bearing inner
  plan is never hashed (PG cannot reach this case for hashed SubPlan because
  of its uncorrelated-ANY restriction; goopg's broader use must check
  explicitly).

## 6. D4.4 — Correlation-projected cache keys

With D4.1, the correct cache key for a correlated SubPlan result is simply
the tuple of its `parParam` slot values — `datumKey` over `len(ParParam)`
datums — namespaced by the subquery node pointer:

```
key = "%p" (expr node) + "\x1f" + datumKey(param₀) + "\x1f" + datumKey(param₁) …
```

This fixes both defects of `subqueryCacheKey(row)` (expr.go:12075):

- **Correctness:** the `collectInValues` collision of §2 disappears — every
  key carries subquery identity (what `nonCorrelatedCacheKey`'s pointer
  suffix already does for the non-correlated case, expr.go:12089).
- **Reuse:** Q2's inner `min(ps_supplycost)` subquery correlates on
  `p_partkey` alone; keying on the full part row (9 columns, all distinct
  per row) can never hit twice, while keying on `p_partkey` hits for every
  re-probe of the same part — and after S1 sorts/joins bring equal keys
  together, adjacent-row hits become the common case. Q17's `avg(l_quantity)`
  per `p_partkey` behaves identically (~200 K distinct keys vs 6 M outer
  rows: a 30× invocation reduction even with a cold cache). [derived from
  plan shapes in evidence/explain-head-e4a43ba6.txt; quantify in S0
  instrumentation]
- Correlated **EXISTS gains a cache for the first time** (§2: it currently
  has none) — subject to the §4.2 cacheability gate (in particular a
  `FOR UPDATE`-bearing EXISTS is never cached). Q4's per-outer-row probes
  over ~unique orderkeys won't hit often, but Q20/Q21-style repeated keys
  will; the handle's `lastParams` fast path (§4.2) covers the adjacent-equal
  case even when the map is disabled.

Eviction and sizing live in D4.5. The key **projection** itself is
semantics-neutral (the projected params are exactly the values the inner
plan can observe); the *caching* it enables is semantics-preserving only
under the §4.2 cacheability gate — both properties are validated against the
chapter 07 matrix (M13 for the gate).

## 7. D4.5 — Cache lifecycle and memory bounds

- **Lifetime:** all SubPlan state (handles, hash tables, result caches) is
  per-`Context`, i.e. per statement — matching today's `SubqueryCache` and
  PG's per-query SubPlanState. Nothing crosses statements; plan-node-pointer
  keys make cross-statement reuse impossible by construction.
- **Scope invalidation:** `SubqueryCacheScope` depth-clearing (context.go:107)
  exists because full-row keys from different nesting depths could collide.
  Param-ID-based keys are globally unique per statement (§3.2), so the
  scope-clear is retired with D4.4; until then it stays.
- **Budget:** one per-statement budget for all SubPlan hash tables and result
  caches combined, tied to `Context.WorkMem` (the `work_mem` GUC,
  `internal/config/defaults.go:641-647`; consumed today by the hash-join
  build, `operators_join_agg.go:418-421`). One case must be pinned
  explicitly: `WorkMem == 0` means "unlimited" (`context.go:195-198`), and
  the hash-join path silently substitutes 512 MiB — the SubPlan caches do
  **not** inherit that silent substitute; they treat 0 as unlimited for
  result caches and use the documented 512 MiB fallback only for the D4.3
  hashability *gate* (which needs a finite bound to decide). Per
  [chapter 06](06-cost-model-touchpoints.md) D6.4 the same number gates
  hashability at plan time. On budget exhaustion: result caches evict LRU;
  a hashed SubPlan over budget falls back to the slice path (never partial
  hash tables — correctness first).
- **Concurrency:** all of this state is written and read by the single
  goroutine executing the query's operator tree. That "one executor
  goroutine per statement" assumption is asserted today by the undisputed
  use of unsynchronized maps on `Context`; S0 instrumentation adds an
  explicit debug-build assertion (goroutine-ID check on Context first-use)
  so parallel-executor work later cannot silently race these caches.
- **Cleanup:** handles keep operators un-`Close`d across rescans (as
  `CorrSubqOps` does today, context.go:113-114 — locks release at commit).
  S2 adds an explicit `Context.CloseSubPlans()` at statement end so pinned
  buffers/iterators are released deterministically instead of by GC; this is
  a behavior tightening, flagged for the executor-feasibility review.

## 8. MultiHashJoin leafFilter sharing

Subquery expressions are opaque to `walkColumnRefs`
(`internal/executor/multi_hash_join.go:386`), so subquery-bearing conjuncts
sink to `leafFilters` and are evaluated at the innermost chain step. Two
invariants for S2:

1. leafFilter evaluation reaches the **same** `subPlanHandle` (keyed by
   planner node pointer) as any other eval path — no duplicate operator
   trees for one subquery node. This already holds structurally for
   `CorrSubqOps` and is preserved by the handle table.
2. A subquery-bearing leafFilter evaluates **at most once per candidate leaf
   combination** — that is the achievable invariant; "once per emitted row"
   is impossible for a filter, since filters decide emission. leafFilters run
   at the recursion base case of `initStepHelper`
   (`multi_hash_join.go:502-507`), and `advanceFrom` re-descends per new
   parent match (:570-612), so **every rejected combination pays a full
   subquery evaluation**, and with today's full-composed-row cache key
   (`subqueryCacheKey` over the MHJ output row) the cache essentially never
   hits. This hazard is confirmable from code at HEAD — S0's per-SubPlan
   counters (chapter 07 §6) *quantify* it; they are not needed to establish
   it. D4.4's projected keys plus the handle table are what actually remove
   the multiplicative cost.

## 9. Phase mapping and acceptance

| phase | contents | acceptance (details in [chapter 07](07-verification-and-measurement.md)) |
| --- | --- | --- |
| S2 SubPlan execution floor | D4.1 param slots + lowering; D4.2 handles + rescan for the §4.2 inventory **including the cacheability gate and its volatility/side-effect classifier**; D4.4 projected keys; D4.5 lifecycle/budget/assertions; retire `CorrSubqOps`/`CorrSubqHashMaps`/scope-clear | Q4 SubPlan path within ~5× of PG's 0.188 s **when forced to SubPlan** (decorrelation disabled), i.e. ≤ ≈1 s; NULL/count-bug matrix green incl. M13 (volatile/LockRows non-caching); no `SubqueryCache` collision reproducible; Q12/Q13 tripwires byte-stable |
| S3 Hashed SubPlan | D4.3 two-table hash, hashability gate, slice fallback; delete linear IN scan | uncorrelated NOT-IN NULL matrix green; Q16-shape (`NOT IN` SubPlan) plan-gate + timing stable-or-better |

S2 deliberately precedes the chapter 03 coverage extensions in value terms:
it protects **every** shape, including the ones the planner will never
legally unnest, and it is what makes "leave it as a SubPlan" a safe planner
decision instead of a cliff (D6.1 depends on this floor existing).

## 10. Open questions

1. **Handle vs interface (D4.2):** confirmed as handle-first here. S7's
   Memoize integrates via the NLI join's own slot-based seam (D5.3, chapter
   05 §5), not via this handle protocol — so the promotion criteria stand
   unchanged: promote the handle protocol into the `Operator` interface only
   when ≥2 non-SubPlan consumers need it.
2. **Param slot spaces (D4.1):** flat per-statement space chosen; verify no
   goopg shape re-enters the same SubPlan recursively (CTE-free today) which
   would need per-invocation save/restore of slots.
3. **Measured floor (S2 target):** the per-invocation cost is currently
   bracketed, not measured (≈5 µs × 1.5 M or ≈130 µs × 57 K — §1). S0's
   counters pin the invocation count and split build-vs-probe time so the
   ≤≈1 s Q4 target can be validated or corrected before S2 lands.
4. **Non-correlated EXISTS hashing (D4.3):** default off pending S3
   measurement — the first-row cutoff may already make it moot.
5. **`CloseSubPlans` (D4.5) — RESOLVED (feasibility review, 2026-07-20):**
   deterministic statement-end close is safe. Operator `Close` never releases
   heavyweight locks (`indexScanOp.Close` clears slices/rows only,
   `operators_index.go:629-640`; `seqScanOp.Close` unpins buffers and records
   stats, `operators_storage.go:1283-1308`); lock lifetime rides the
   lockmgr's statement/txn scope independently of Close, so
   `Context.CloseSubPlans()` cannot disturb the commit-time assumption at
   context.go:113-114. Two requirements carried into S2: Close must be
   idempotent (eval sites may already have Closed fallback-path operators),
   and LockRows-bearing subplans follow the §4.2 gate (never cached; rescan
   re-executes). `existsImpl`'s `maxDrain=1` cutoff survives handle-ization
   (set once at build).
