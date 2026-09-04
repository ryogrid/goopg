# A-01(ii): full-rtable-order EXPLAIN numbering via planner-stamped range-table identity

Status: design only — no code changed, nothing committed.
Pointers: `TODO_ALL.md` A-01 (P0-04 suffix numbering); take3 08 §3; take3 04 §11
(EXPLAIN rendering deltas); take2 `impl/P0-A-explain-instrument.md` §5 (the four-point
PG-rule correction); `internal/executor/explain_names.go`; `internal/optimizer/planner.go`
(`planFromClause`, `planFromRangeVars`, `planFromItem`, `planScanRangeVar`,
`planSubqueryRangeVar`, `planSelectWithParent`); `internal/optimizer/plan.go` (scan nodes);
`internal/executor/operators_explain.go` (`planChildren`).

## 1. What is broken today

`explainNames` (`internal/executor/explain_names.go`) keys everything off
`SchemaColumn.SourceTableIdx`, a per-FROM-clause monotonic id (M0071-0009) whose counter
(`nextSourceIdx`, `planner.go:2518,2572`) **restarts at 1 for every query level**
(`planFromClause` / `planFromRangeVars` / `planFromItem`). Three consequences, in
severity order:

1. **Collision (correctness).** An outer-scope binding and a subquery-internal base
   relation can carry the same `SourceTableIdx`. A wrong qualifier is worse than none,
   so `cols` (`:52-62`) refuses to qualify unless the column name exists in the claimed
   relation, and `seen` (`:46-49`) keeps first registration. The complete fix was always
   deferred to planner work: "a globally unique range-table id (PostgreSQL's varno)"
   (`:60-61`).
2. **Survivors-only ordering (numbering divergence).** `collect` (`:129-179`) gathers
   only scan-like nodes present in the plan and sorts by `SourceTableIdx`. PG numbers
   over the **full range table in rtindex order, including entries the plan never
   materialises** (take2 P0-A §5 point 4). Any eliminated RTE shifts every later suffix.
   This is the likeliest source of the residual `shape_mismatches` attribution error
   (take3 07 §2.2, 04 §11).
3. **Naive counter (edge-case divergence).** `taken` (`:43-45`) is a plain per-base
   ordinal. PG's `NameHashEntry.counter` is a never-resetting high-water mark with a
   `do/while` collision re-check, so a literal alias `x_1` pushes the second `x` to
   `x_2`, and a generated `x_1` is itself entered as a base (take2 P0-A §5 point 1).
   Parent-namespace preload (point 2) is deferred there for lack of a query-level
   notion in the renderer — this design supplies that notion on the planner side
   instead (see §6 for what is still accepted as divergent).

A-01(i) status, verified while reading: `IndexOnlyScan` **already carries `Alias`**
(`plan.go:945-951`, "Mirrors IndexScan.Alias (M0062-0002)"); the two min/max-agg
synthesis sites deliberately leave it empty. Sub-item (i) needs no new stamping work —
only a check that every `IndexScan→IOS` promotion site copies it (the struct comment
claims they do; the first cut re-verifies by test, not by audit).

## 2. PG's rule (the target, no more, no less)

There is no `select_rtable_names`; it is `select_rtable_names_for_explain`
(`ruleutils.c:3855`), a frontend to static `set_rtable_names` (`:3884`):

- Base name: FROM-clause alias first, else the live catalog relation name
  (`get_rel_name`, not `eref->aliasname`); unreferenced RTE / unnamed join → `NULL`.
- First occurrence bare, later ones `_N` — but `_N` is **not** an occurrence ordinal:
  high-water counter per base name, candidate re-checked against the hash, generated
  names entered as bases.
- Numbering runs over the **whole rtable in rtindex order** with `NULL` entries kept
  1:1; the hash is preloaded with parent-namespace names, so an inner level's first
  occurrence can still be suffixed.

## 3. Where the global counter lives

### 3.1 Owner and scope

New type in `internal/optimizer`, e.g. `rtableScope{ next int32 }`, created **once per
top-level statement in `Plan()`/`PlanSchemaOnly`** (`planner.go:85ff`/`51ff`)
and threaded down. It is created there and NOT at the `planSelectWithSettings`
head (`planner.go:811`): that function runs on *every* recursion (set-op
branches at `:929`/`:957`/`:1061`, CTE bodies at `with.go:205`/:264/:334/:395,
`copy.go:41` all delegate to it), so creating the scope at `:811` would fork
the counter per level — the exact bug being fixed (review F1).
`planSelectWithParent` (`planner.go:13827-13848`) takes the scope as an
explicit second channel alongside `planParent`. It is *not* hung off
`PlannerSettings`
(a by-value struct copied at every call site — a counter there would fork), and *not*
a package global (the `planParent` pattern, `planner.go:13867-13878`, is already
documented as goroutine-thread-unsafe technical debt; duplicating it for a second
channel would be indefensible).

### 3.2 Threading

Replace the `nextSourceIdx *int16` parameter with the scope pointer (or carry both
during migration) along the exact chain that already threads the counter today:

```
planSelectWithSettings → planFromClause / planFromRangeVars
  → planFromItem (*int16 today, :2735) → planScanRangeVar (sourceIdx int16, :2995)
```

Plus every re-entrant planning path that today restarts numbering implicitly:

- **Derived tables**: `planSubqueryRangeVar` (`planner.go:4063-4187`) →
  `planSelectWithParent` (`planner.go:13827-13848`, NOT `:4063-4113` as
  earlier mis-cited — that range is `planSubqueryRangeVar` itself, review
  F2). `planSelectWithParent` currently takes only `(stmt, cat, parent)`;
  it gains the scope param (or the scope rides alongside `planParent` as
  an explicit second arg — same edit, no new global). Must survive
  `*lateralCtx` copies (`planner.go:4076`): a pointer field copies fine,
  just never store the scope by value.
- **Sublink subqueries** (`EXISTS` / `IN` / scalar / `planInExpr` paths —
  specifically the four Expr-level planners `planSubqueryExpr` /
  `planArraySubqueryExpr` / `planInExpr` / `planExistsExpr`,
  `planner.go:13596-13725`, review F4) and **CTE bodies**
  (`preplanWithClause`, `with.go:91`; all CTE-body `planSelect` calls at
  `with.go:205`/`:264`/`:334`/`:395`; DML-CTE bodies at `with.go:576`
  → `planInsert/Update/Delete/Merge`; `planInsert`'s SELECT at
  `planner.go:10952`; UPDATE multi-assign subquery at `:11634`;
  `PlanSchemaOnly` at `:58`; grouping operand at `:1061`): same
  treatment — they plan nested `SelectStmt`s that consume FROM entries.
- **Set-op branches**: planned via recursive `planSelect` on the same statement —
  they share the scope (uniqueness is what matters; PG numbers each branch's rtable
  separately, but goopg renders one flat plan, so one flat namespace is the accepted
  model — §6).
- **DML FROM/USING lists (F5)**: `planUpdate` FROM scans use a separate
  local counter (`planner.go:11727-11733`: target = 1, FROM = 2,3,… via
  `planScanRangeVar`; same pattern presumably in Delete/Merge). These never
  flow through `planFromClause` and restart independently of the SELECT
  scope. Thread the scope there too, or record the exclusion with reason —
  currently the design is silent and that is fixed here by naming it.

Allocation order = first-encounter order during planning (outer FROM left-to-right,
then nested as reached). Deterministic given (statement, catalog), hence plan-cache
safe: a cached plan carries stamps that re-planning reproduces exactly (cf. the P2-04
cross-session cache note in `plannersettings.go:17-19` — the requirement is only that
stamps be a pure function of statement+catalog, which they are; no session state may
feed the allocator).

### 3.3 What gets stamped

Additive field, e.g. `RTID int32` (name TBD at implementation), on the stamped nodes
(§4). `SourceTableIdx`, `rangeBinding.sourceIdx`, and every `(Name, SourceTableIdx)`
rebind in unnest/NLI/joinlayout are **untouched** — the executor's value paths keep
reading exactly what they read today. That is what makes "values unaffected by
construction" (§8) a structural claim, not a test claim: the only consumer of the new
field is `explain_names.go`.

`0` keeps its current meaning ("no identity"): subquery-derived columns
(`planSubqueryRangeVar` leaves inner output columns at 0,
`planner.go:4175-4182` — planner.go, not plan.go) and legacy callers stay
unqualified, exactly as today. Limits of the fallback (review F8): the
direction is safe (`register` early-returns on 0; `explainSingleSourceIdx`
skips 0s), but "degrades to unqualified" is proven only for all-zero
nodes — a mixed node (some outputs 0) registers under the nonzero id with
`cols` covering derived names too, so a later `column(src=X, derivedName)`
lookup can wrongly qualify (low-probability, recorded); and
`resolveInAncestor` keys on bare base names, unsuffixed and id-free, so it
can disagree with `bySource` suffixes (safe direction: ambiguous → `""`,
but not id-keyed).

## 4. Which node types get stamped

**Minimal set closing the collision hazard** (must-have): every node type
`explainRelBaseName` already reads — `SeqScan`, `IndexScan`, `IndexOnlyScan`,
`CTEScan`, `MaterializedCTEScan` — **plus `BitmapHeapScan`**. The last one is a real
gap found while reading: `BitmapHeapScan` already carries `Alias` (`plan.go:2661`)
but is covered by *neither* `explainRelBaseName` nor `explainIsScanNode`, so bitmap
heap scans neither contribute a base name nor get scan-qual rendering. Stamping it
without extending those two switches would stamp a field nobody reads; the cut must
do both together (two-line switch extension, covered by the same unit test).

**Deferred with stated reason, not stamped:**

- `BitmapIndexScan` (has `Table`+`Alias`, `plan.go:2635-2648`): renders *beneath* the
  heap scan as `Recheck Cond` machinery, never as a named relation in PG. No stamp.
- `WorkTableScan` (no name fields at all, `plan.go:2623`): recursive self-reference
  renders as a leaf by `planChildren`'s explicit stop (`operators_explain.go:2824-2830`).
  No stamp.
- Function/tablefunc nodes (`FromUnnest`, `PgGetPublicationTables`, `UserSrfScan`,
  `RowsFrom`, …), `Values`, childless `Result`: no relation identity in PG's
  `set_rtable_names` sense either (unnamed → `NULL`). No stamp; `src == 0` fallback
  keeps today's rendering. DECISION REQUIRED at implementation (review F6):
  `planValuesSubquery` (`planner.go:3961`), `planTableFuncRangeVar` (`:4804` →
  RowsFrom/SRFs) and the ordinality wrap (`:4718`) all consume `sourceIdx`
  today — PG *would* count those RTEs, so consuming an RTID is arguably
  correct, but unstamped ids then create numbering holes vs the rendered
  names, while not consuming breaks 1:1 correspondence with PG rtindex.
  Either choice moves suffixes; the cut must pick one and record it
  (recommendation: consume — holes are PG-faithful, correspondence loss
  is not).
- Join/Append/SetOp nodes: PG names `RTE_JOIN` from `eref->aliasname`, unnamed → NULL;
  goopg has no join RTEs. Out of scope.
- **Fan-out (review F7)**: parent + inheritance children share one
  `sourceIdx` today (`planner.go:3254-3289`, all
  `tableSchemaWithSource(..., sourceIdx)`), and the same holds for the
  partitioned-table fan-out (`:3216ff`). The cut gives EACH LEAF its own
  RTID (fine vs PG, which counts RTEs not leaves — but this is a behavior
  change from 1 id per FROM entry to N, interacting with F6), and the
  re-pin diff needs reviewer eyes on every multiplied label.

**Full-fidelity option explicitly rejected**: stamping eliminated-join or pulled-up
RTE placeholders that have no node. There is nowhere planner-honest to put them —
goopg has no rtable — and inventing phantom nodes for the renderer to count would
couple EXPLAIN shape to planner internals. The residual divergence this leaves is
declared in §6 instead.

## 5. How `explain_names` migrates

1. **Key by the global id.** `bySource`/`taken`/`seen`/`cols` keep their shape; the key
   domain changes from per-level `SourceTableIdx` to statement-unique RTID. The
   `sort.SliceStable` by key in `collect` then yields allocation order ≈ FROM order
   across levels, replacing today's within-level-only order.
2. **Keep `SourceTableIdx` as the storage type's fallback, not the key.** Column
   resolution still arrives via `SchemaColumn.SourceTableIdx`; the bridge is
   `explainSingleSourceIdx`-shaped (all output columns share one identity) but reads
   the new RTID field instead. `src == 0` → unqualified, unchanged.
3. **Keep both guards.** `seen` is still load-bearing: CTE bodies are *shared*
   pointers across references (`CTEScan.Child` aliases one planned body), and
   `NestedLoopIndexJoin.InnerMemo.Child` aliases `Inner` — the same node is reachable
   twice and must register once. `cols` stays as cheap defence-in-depth (it also
   covers the `SourceTableIdx==0`-only legacy nodes).
4. **Adopt the high-water counter** (take2 P0-A §5 points 1–2: `do/while` re-check,
   generated names entered as bases). On parent-preload (point 3): global
   uniqueness does NOT subsume it (review F9) — if the outer entry holding
   a name produces no node (eliminated/pulled-up), goopg consumes nothing
   while PG's preload still suffixes the inner first occurrence. Same
   family as the §6 delta; listed there, not claimed away here.
5. **`nodeLabels` (M0128-P5.1) stays a separate per-node pass**, still keyed by
   `nodePtr`, but iterated in RTID order instead of `src` order so label suffixes and
   column-qualifier suffixes agree with each other. The SEMI-join-same-relation case
   in its comment is unchanged.

## 6. PG-fidelity limit (stated, not fixed)

**Pulled-up subqueries and eliminated joins leave no node in goopg's plan, and
numbering WILL still diverge from PG there.** PG counts rtable entries goopg never
materialises (a pulled-up subquery's former RTE slot, a join-removed RTE); goopg can
only number surviving scan nodes. Global ids fix *which relation* a qualifier names
(the correctness hazard) and stabilise *relative* order among survivors; they cannot
reproduce absolute `_N` values wherever the two rtables have different membership.
Concretely: any query where PG's plan contains no node for an rtable entry goopg also
dropped may still show `t_1`-vs-`t_2`-style suffix drift. This is a renderer-label
delta only — row values, join shapes, and costs are untouched — and the re-measurement
(§8) quantifies it instead of asserting it away.

## 7. Hazards

- **Parallel plan nodes.** `stampParallelScan` (`parallel.go:380ff`) *does*
  clone (`c := *x; return &c`) — the design's "assert never clones" asked
  the wrong question (review F10). Harmless: the shallow copy carries any
  stamp field automatically. The REAL invariant is ORDERING: RTID stamping
  must precede Gather construction / `stampParallelScan`. (Side note: that
  switch handles Seq/BitmapHeap/IOS/Filter/Project/Join but not IndexScan —
  confirm whether an IndexScan under Gather can miss the "Parallel " prefix
  today; adjacent, out of scope.)
- **Cached plans.** Stamps must be a pure function of (statement, catalog) — §3.2.
  No timestamp, no session GUC, no map-iteration order may feed allocation. The
  existing `nextSourceIdx` discipline already satisfies this; the scope preserves it.
- **Rescan / re-Open re-stamping.** The stamp is written once, during planning, on the
  plan node; the executor treats plan nodes as read-only at Open/Rescan time.
  No executor write path may touch the field (grep-guard in the cut's unit test:
  assignments to the field outside `internal/optimizer` fail the test).
- **SubPlan bodies (F3 — the miss is total, not per-kind).** `explainNames.collect`
  walks only `planChildren` (Node children), but scalar/`IN`/`EXISTS`/`ARRAY`
  multi-assign sublink bodies hang off *Expr* fields (`SubqueryExpr.Plan`,
  `InExpr.Plan`, `ExistsExpr`, `ArraySubqueryExpr`, `MultiAssignSubqRow` —
  `plan.go:317-357`, planned at `planner.go:13596-13725`). NONE are visited
  today, so no sublink-internal scan registers — not just childless-`Result`
  InitPlans. Cut 2's "TPC-DS Q30-shape now qualifies" test FAILS unless the
  cut also extends the walker (or adds a second collector over Expr
  subplans). §8 lists the walker work item (added here, was missing).
- **CTE body sharing.** One planned body, N `CTEScan` consumers, each with its own
  RTID; the body's *inner* scans carry one RTID registered once via `seen`. First
  registration wins — with global ids the winner is unambiguous (no cross-level
  collision to break ties any more), which is precisely the upgrade.
- **int16 overflow.** `SourceTableIdx`/`sourceIdx` are `int16` end to end. The new
  field should be `int32` (or wider) so the identity space is not the constraint;
  statements exceeding int16 RTEs already break elsewhere and are out of scope.
- **Inheritance / partition expansion.** One FROM entry fans out to many leaf scans
  (cf. `SkipIfVanished`/`InheritParentOID`, `plan.go:706-725`). PG gives each expanded
  RTE its own rtindex. Decision for the cut: each leaf scan consumes its own RTID
  (matches PG; also matches one-scan-one-row in EXPLAIN). Record the choice in the
  commit message — it moves labels on partitioned-table plans.

## 8. Cut order

1. **Scope + FROM-path threading + stamp fields** (`rtableScope`, `planFromClause` /
   `planFromRangeVars` / `planFromItem` / `planScanRangeVar`, new RTID field on the
   §4 minimal set). Subquery/sublink/CTE paths not yet threaded: inner scans get
   RTID 0 → today's unqualified rendering. Safe by fallback. Unit test: top-level
   self-join gets distinct ids; nested scans stay 0.
2. **Thread the re-entrant paths** (`planSelectWithParent` incl. the four
   Expr-level sublink planners, `preplanWithClause` incl. DML-CTE bodies,
   set-op recursion, DML FROM/USING or recorded exclusion). Extend the
   `collect` walker to Expr-hanging subplan bodies (F3) or add the second
   collector. Unit test: TPC-DS Q30-shape — outer binding
   and inner same-name relation no longer share an id; `cols`-guard case from the
   M0125-0039 deferral row now qualifies instead of degrading.
3. **`explain_names` migration** (§5: re-key, high-water counter, `nodeLabels` order,
   `BitmapHeapScan` switch extension). Unit tests: PG-rule cases — alias-first,
   first-bare, literal-`x_1` collision → `x_2`, SEMI-join label split. Re-pin the
   goopg-vs-goopg baseline **in the same commit** (take2 P0-A §9: label diff reviewed
   line by line, every change traceable to the rtindex-order rule).
4. **Re-measure.** Spine `shape_mismatches` re-run per take3 04 §11; correct the 07 §2
   attribution (expect: barely moves or moves on exactly the eliminated-RTE queries —
   either outcome is recorded as a finding, take2 P0-A §10).

## 9. Falsifiable gate

- **Units** (cut 1–3 above): global-uniqueness across levels; PG-rule suffix cases;
  no-assignments-outside-optimizer guard; all `IndexScan→IOS` promotion sites preserve
  `Alias` (closes A-01(i) by test).
- **PP (plan pin)**: goopg-vs-goopg baseline re-pinned in the cut-3 commit; the
  before/after label diff is reviewed line-by-line with every hunk traceable to §2's
  rule. Normalising away relation labels, captures are otherwise byte-identical —
  any structural/cost drift is a planner leak and reverts.
- **Spine**: `shape_mismatches` re-measured (take3 04 §11); the 07 §2.2
  "upper bound" attribution corrected with the new number.
- **Values unaffected by construction**: no executor/eval/catalog path reads the new
  field — only `explain_names.go`. The TPC-H/TPC-DS row-count oracles cannot move;
  if one does, the cut is wrong, not the oracle.

## 10. Open risks

1. The §6 divergence may cover most of the queries the re-measurement was supposed
   to fix (if `shape_mismatches` is dominated by eliminated-RTE drift, global ids
   move the count but do not zero it). Mitigation: none available without an rtable —
   that is the finding to record.
2. `planChildren` may not reach every subplan-body kind (§7); each unreached kind is
   silent unqualified rendering, discoverable only by per-kind tests (take3 08 §3 R3:
   renderer arm per node type).
3. Partition/inheritance fan-out (§7) multiplies labels on plans the baseline pins;
   the re-pin absorbs it, but the diff needs reviewer eyes, not just `changed=N`.
4. Threading the scope through *every* re-entrant path is a hunt (sublink planners,
   DML, MERGE, `RowsFrom`); a missed path degrades to RTID 0 silently. Mitigation: a
   planner-level test asserting no scan node in a corpus plan (TPC-H 22 + TPC-DS 99)
   carries RTID 0 unless its columns are legitimately identity-free (subquery-derived
   at 0 by design, §3.3).
