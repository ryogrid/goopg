# P0-A — EXPLAIN as a measurable instrument

Implementation design for TODO items **P0-01, P0-02, P0-03, P0-04, P0-04b,
P0-04c**. Parent design: [08 §3](../08-target-design.md); gates:
[09 §3, §5](../09-verification-and-acceptance.md).

Revision 2 — corrected after agent review. The review found a **blocker in the
mechanism revision 1 chose** (§3), two arms that defeat the coverage test (§2),
a third render site the design ignored (§3.5), and four factual errors about
PostgreSQL. Each is marked **[R2]**. Corrections that belong in the parent
bundle are propagated by the commit that lands this file.

---

## 1. Why this group comes first

Every later phase is judged by a plan diff against PostgreSQL. Today goopg's
`EXPLAIN` cannot express the two things such a diff needs:

1. **Costs.** Both text walkers print the literal string
   `cost=0.00..0.00 … width=0` (`internal/executor/operators_explain.go:494`,
   `:1540`). The chosen `Path` carries a real `Cost{Startup,Total}`
   (`internal/optimizer/path.go:36-39`) and `Rows` (`:105`), and those numbers
   are discarded at `createPlan` time. No artefact in the repository states what
   the planner believed, so no artefact can attribute a wrong plan to a wrong
   cost.
2. **A trustworthy label.** A census over EXPLAIN text measures the labeller,
   not the planner (09 §1 R3). `describePlan`'s fallthrough is
   `fmt.Sprintf("%T", n)` (`:2224`), so an uncovered node type renders
   `*optimizer.Foo` — a string no PG plan contains.

**No planner behaviour changes in this group.**

---

## 2. P0-01 — node-type EXPLAIN coverage test

### Problem

`describePlan` (`:1958`) is a **42-arm** type switch — **[R2]**, revision 1 said
28 — with a `%T` fallthrough at `:2224`. Nothing fails when a node type is added
without an arm.

### Design

A test in `internal/executor` that:

1. Enumerates plan-node types by parsing `internal/optimizer`'s non-test `.go`
   files with `go/ast`: a type qualifies when it declares both `Pos() int` and
   `Output() Schema`. That is exactly `optimizer.Node` (`plan.go:18`), so the
   enumeration cannot drift from the interface. **Verified sound**: all 60 such
   types are declared in `plan.go`, all with pointer receivers, and the
   `Pos()`/`Output()` sets are identical.
2. Constructs a zero value via `reflect.New(t)` and calls `describePlan` and
   `describePlanVerbose`. (`describePlanVerbose` at `:1880` is a 12-arm switch
   that falls through to `describePlan` at `:1956` — **[R2]** it has no `%T` of
   its own, so calling both is belt-and-braces, not two independent checks.)
3. Fails when the result begins with `*optimizer.`.

A panic counts as covered: reaching an arm is what the test asserts. **[R2]**
Revision 1 justified this with "the fallthrough is the only path that cannot
panic", which is false — many arms return constants (`"Filter"`, `"Sort"`).
The correct justification is simply that a panic proves an arm was entered.

### **[R2]** Two covered arms return `%T` themselves — fix them, don't exempt them

`describePlan` has **three** `%T` returns, not one:

- `:2150` — `*optimizer.BitmapHeapScan` when `p.Table == nil`
- `:2159` — `*optimizer.BitmapIndexScan` when `p.Index == nil`
- `:2224` — the fallthrough

A zero value has exactly those nil fields, so both **covered** arms would be
reported uncovered. The fix is not to special-case the test: a nil `Table` on a
`BitmapHeapScan` is a producer bug, and printing a Go type name for it is the
same defect P0-01 exists to kill — as that arm's own comment (`:2143-2148`)
records, this node printed `*optimizer.BitmapHeapScan` in production and made a
plan census read zero while bitmap scans were being chosen. Both arms return a
stable label (`"Bitmap Heap Scan"` with no `on <rel>` suffix) instead.

### **[R2]** The test fails today, on 18 types

60 types declare `Output() Schema`; `describePlan` has 42 arms. Uncovered:

`Call`, `DistinctOn`, `FromRegexpMatches`, `FromRegexpSplitToTable`,
`PgAvailableWalSummaries`, `PgGetCatalogForeignKeys`, `PgGetPublicationTables`,
`PgGetSequenceData`, `PgInputErrorInfo`, `PgOptionsToTable`, `PgPartitionTree`,
`PgSequenceParameters`, `RecursiveUnion`, `RowsFrom`, `ScalarFuncScan`,
`TSTokenType`, `VerifyHeapam`, `WorkTableScan`

Several are unambiguously plan-renderable — `RecursiveUnion` and
`WorkTableScan` are PG's `Recursive Union` and `WorkTable Scan`, and a
`WITH RECURSIVE` query in either suite would render a Go type name today.
Revision 1 said "unknown at design time whether the test passes"; it does not.
All 18 get arms in the **same commit**, since a test that lands red is not a
gate. Set-returning-function wrappers that PG renders as `Function Scan` are
labelled as such rather than invented.

*Gate: units.*

---

## 3. P0-02 — real costs on the plan node

### **[R2] Revision 1's mechanism does not work**

Revision 1 chose "a side index built by `createPlan`, carried on the `Explain`
wrapper", and claimed this preserved `Plan(stmt, cat) (Node, error)`. The review
showed the two halves are incompatible. `Explain` is built at
`internal/optimizer/planner.go:278` from `inner, err := Plan(explainInner, cat)`
(`:275`), and there is **no per-statement context object** anywhere on the chain

```
planStmt → planJoinlistSearch (relfromjoinlist.go:150) → makeRelFromJoinlist (:257)
         → searchOneProblem (:336) → createPlanAtSearchRootRange (createplanroot.go:108)
         → createPlanNode (createplan.go:44)
```

`joinlistProblem` is per-search, not per-statement. Worse, `createPlanNode`
returns its `outputLayout` **bottom-up** (`func createPlanNode(p *Path) (Node,
outputLayout)`), so "threaded the same way" described the wrong direction for an
accumulator. A side index could reach `planner.go:278` only via a package-global
(which revision 1 correctly rejected as a pointer-keyed leak, unsafe under
concurrent planning) or by changing `Plan`'s signature.

There was also a second, independent defect: **node reuse is real.**
`internal/executor/explain_cte.go:107-121` records that a second `CTEScan`
referencing an already-claimed CTE shares the same child — "the body is the same
buffer". A `map[Node]PlanCost` collapses two tree positions into one entry.

### **[R2] Revised mechanism: the cost rides on the node**

This is what PostgreSQL does — `startup_cost`, `total_cost`, `plan_rows`,
`plan_width` are fields of `struct Plan`
(`postgres/src/include/nodes/plannodes.h`) — and it dissolves both defects: no
channel is needed, and a shared node legitimately has one cost because it *is*
one node.

`internal/optimizer` gains:

```go
// PlanCost is PG's Plan.startup_cost / total_cost / plan_rows / plan_width
// (postgres/src/include/nodes/plannodes.h, struct Plan). Embedded in the plan
// nodes the path search produces, so a node carries the estimate that chose it.
type PlanCost struct {
    StartupCost float64
    TotalCost   float64
    PlanRows    float64
    PlanWidth   int
    CostSet     bool
}

func (c *PlanCost) PlanCostInfo() (PlanCost, bool) { return *c, c.CostSet }
```

`PlanCost` is **embedded** in the node types `createPlanNode` produces, so each
gets the accessor by promotion and the renderer needs only

```go
type costedNode interface{ PlanCostInfo() (optimizer.PlanCost, bool) }
```

Field names are deliberately `StartupCost`/`PlanRows`/… rather than
`Startup`/`Rows` so embedding cannot collide with an existing field (`Values`
already has `Rows`). All goopg node literals are keyed, so adding an embedded
field breaks no construction site.

Scope: the 10 node-producing arms of `createPlanNode` (`createplan.go:44` has 12
cases, 10 producing, 2 panicking on `PathMemoize`/`default`). Every other node
falls to §4's derivation. Phase 4/6 extends the embedding upward as upper-rel
paths arrive.

### **[R2]** `rows=` keeps `EstimateRows` in this commit

The render call is

```go
label += fmt.Sprintf("  (cost=%.2f..%.2f rows=%d width=%d)",
    c.StartupCost, c.TotalCost, est, width)
```

where `est` is **still `optimizer.EstimateRows(rowSrc)`**, unchanged. Revision 1
left `rows` undefined in its snippet. Sourcing it from `PlanRows` would change
the rows column for every indexed node — a planner-visible change smuggled into
an instrument commit, breaking §9's exit check and the `estimate-audit`
comparison. Switching `rows=` to `PlanRows` is a **separate, later item**
(P1-16's neighbourhood) with its own before/after.

`COSTS OFF` suppression already exists at both sites and is unchanged.

### **[R2]** Width: use the function that already exists

Revision 1 proposed computing width from a formula it stated as "a varlena
contributes `32 + sizeof(int32)`". **That formula does not exist in
PostgreSQL**, and goopg already implements the real one.

`get_typavgwidth` (`postgres/src/backend/utils/cache/lsyscache.c:2718-2760`):
`typlen` when positive (`:2726`); else via `type_maximum_size` — BPCHAR →
`maxwidth` (`:2741`); `maxwidth <= 32` → `maxwidth` (`:2743`); `< 1000` →
`32 + (maxwidth-32)/2` (`:2745`); else `516` (`:2753`); unbounded → plain **32**
(`:2759`), with no header added.

goopg reproduces this in `internal/optimizer/relsize.go`: `typeWidth` (`:254`),
`typAvgWidthFromMax` (`:357`), `varlenaDefaultWidth = 32` (`:232`),
`typAvgWidthCap = 1000` (`:237`), and `tupleWidth(cols []SchemaColumn) int`
(`:376`) — whose doc comment already names "the EXPLAIN width column" as a
consumer it does not yet have.

So the width work is: **export `tupleWidth` (or compute width inside the
optimizer) and call it.** No new formula.

**[R2]** Revision 1 also claimed `set_rel_width` consults `get_typavgwidth` and
prefers `stawidth`. The precedence is the *caller's*: `set_rel_width`
(`costsize.c:6210`) tries cached `rel->attr_widths` (`:6257`), then
`get_attavgwidth` (`:6264`, the only reader of `stawidth`,
`lsyscache.c:3298-3321`), and only then `get_typavgwidth` (`:6275`). A
stawidth-preferring path is **not** attempted here, because
`catalog.ColumnStats.AvgWidth` (`internal/catalog/catalog.go:1823`) is
documented as the average width of the *variable payload per non-null value* —
not PG's `stawidth` semantics — while `pg18_user_catalog_rows.go:1634` writes it
out *as* `stawidth`. Resolving that semantic mismatch is Phase 1 work
(P1-11's neighbourhood) and is recorded as a deferral, not silently absorbed.

### **[R2] §3.5 — there is a third render site, and it emits no costs at all**

`FORMAT {JSON,XML,YAML}` routes through `planToJSON`
(`operators_explain.go:1775`, dispatched at `:97` and `:132`), which sets
`"Plan Rows"` (`:1791`) and **no** `Startup Cost`, `Total Cost` or `Plan Width`.
PostgreSQL emits all four. After P0-02 as revision 1 scoped it,
`EXPLAIN (FORMAT JSON)` would still carry no cost and would now *disagree with
TEXT* — a new asymmetry created by the fix for an old one.

`planToJSON` also **does not collapse `*Project`/`*Filter` wrappers** (it
recurses via `planChildren`, `:1801-1803`), so the JSON tree has a different
shape *and* a different `rows=` from the text walker.

Both are in scope for this group: `planToJSON` gains the four cost keys from the
same `costedNode` assertion, and the wrapper-collapse divergence is recorded as
its own item. The parity instrument (P0-B) parses **text**, so the JSON shape
gap does not block it — but leaving it undocumented would let a later phase
"fix" JSON and think it had changed the planner.

### **[R2]** What actually invalidates downstream

Revision 1 said the `estimate-audit` "2026-08-05 reference capture" is
invalidated. Wrong: that reference is the **PG 18.3 side**
(`cmd/estimate-audit/main.go:225-229`, `--reference`/`--ref-port` at
`:288-289`) — PG's own EXPLAIN text, untouched by goopg's renderer.

The parser needs no update either: `internal/testutil/estimateaudit/audit.go:51`
accepts arbitrary cost and width and captures only `rows`. A sweep confirmed
nothing in `internal/testutil/estimateaudit/` or `cmd/estimate-audit/` asserts on
`width=0` or `0.00` outside hand-written test fixtures, and no regress `.out` or
golden file contains `(cost=`. `cmd/plan-snapshot/main.go:349`'s `rowsRegexp` is
the same shape and likewise unaffected.

What **does** invalidate on every query are the **goopg-vs-goopg** baselines:
`make plan-gate`'s snapshot and the TPC-DS SF05 `plans-*.txt` channel. Both are
re-pinned by the same commit.

The revision-1 precondition "do not start P0-02 until P0-05 and P0-06 land" is
withdrawn — it existed to preserve a fallback signal that is not lost.

*Gate: units; a test comparing rendered numbers against `finalPath()`;
`estimate-audit` green; both goopg-vs-goopg baselines re-pinned.*

---

## 4. P0-03 — nodes above the seam

### Problem

Everything above the search — `Aggregate`, `Sort`, `Limit`, `Project`, `SetOp`,
`WindowAgg`, `Distinct`, `LockRows`, `Gather`/`GatherMerge`, `Merge`,
`CTEDMLPrefix` (**[R2]** revision 1's list omitted the last five) — is built by
the legacy rewriter, carries no `PlanCost`, and would render zeros. A capture
mixing real costs with `0.00` is worse than one where all are `0.00`: a
normaliser cannot tell a free node from an unpriced one.

### **[R2]** Where it lives

Revision 1 put `deriveCost` "in the renderer" and had it read
"the constants already in `defaultCostParams()`". `costParams`
(`internal/optimizer/cost_funcs.go:47`), `defaultCostParams()` (`:83`) and the
fields `cpuTupleCost` (`:87`) / `cpuOperatorCost` (`:89`) are **unexported**;
`internal/executor` cannot see them.

So the function lives in `internal/optimizer` as
`DeriveLegacyDisplayCost(n Node) PlanCost` and is called by the renderer.
(`optimizer.EstimateRows` is already exported, `cardinality.go:43`.)

### Rule

Not a new cost model — the existing legacy estimate, made visible:

- `PlanRows` = `EstimateRows(n)`, the number the line already prints.
- `TotalCost` = child total + `cpuTupleCost × rows` for a pass-through wrapper;
  `Sort` adds `cost_sort`'s comparison term; `Aggregate` adds `cpuOperatorCost`
  per input row per aggregate.
- `StartupCost` = the child's startup for pass-through nodes; the child's
  **total** for blocking nodes — **[R2]** which are `Sort` and hashed
  `Aggregate` only. Revision 1 also listed `Materialize`, wrongly twice:
  `cost_material` (`costsize.c`) sets `startup_cost = input_startup_cost`, the
  child's *startup*; and goopg has **no** `Materialize` plan node (the only
  `Material*` type is `MaterializedCTEScan`).

Deliberately crude, labelled as such in the code, deleted by Phase 4 when every
one of these nodes becomes a real upper-rel path. A test asserts no node in the
regress corpus renders `cost=0.00..0.00`.

*Gate: units.*

---

## 5. P0-04 — relation-name suffix numbering

### **[R2]** The parent bundle's model of PG's rule is wrong in four ways

De-duplication already exists in goopg (`internal/executor/explain_names.go`:
`register` `:189`, `nodeLabels` `:167-177`) — as **07 §2.2 already states**, so
revision 1 presenting this as its own correction was a restatement.

Today's goopg rule: collect scan-like nodes, `sort.SliceStable` by
`SourceTableIdx` (`:153`), first occurrence of a base name bare, later ones
`_1`, `_2`, … from a plain per-base counter; base name is alias-first else
lowercased catalog name (`explainRelBaseName`, `:211-245`), covering only
`SeqScan`, `IndexScan`, `IndexOnlyScan`, `CTEScan`, `MaterializedCTEScan`.

PostgreSQL's actual rule — and there is **no function `select_rtable_names`**;
it is `select_rtable_names_for_explain`
(`postgres/src/backend/utils/adt/ruleutils.c:3855`), a frontend to the static
`set_rtable_names` (`:3884`) where the logic lives:

1. The suffix is **not a per-occurrence ordinal**. `NameHashEntry.counter`
   (`:316-321`) is a per-base-name high-water mark that never resets, and the
   candidate `name_N` must itself be absent from the hash —
   `do { hentry->counter++; … } while (found);` (`:3987-4004`). If a literal
   alias `x_1` exists, the second `x` becomes `x_2`; a generated `x_1` is
   entered as its own base (`:4005`), so a later literal `x_1` becomes `x_1_1`.
2. "First occurrence gets no suffix" is **false across query levels**: the hash
   is preloaded with parent-namespace names (`:3911-3928`), so a first
   occurrence at an inner level can still get `_1`.
3. Base-name precedence (`:3942-3966`): unreferenced RTE → `NULL`; **alias
   first** (`:3947` — the one point revision 1 had right); `RTE_RELATION` →
   `get_rel_name(relid)` (`:3952`, the live catalog name, *not*
   `eref->aliasname`); unnamed `RTE_JOIN` → `NULL`; else `eref->aliasname`.
4. PG numbers the **whole range table, in rtindex order**, appending `NULL`
   entries to keep `rtable_names` 1:1 with the rtable. goopg orders by
   `SourceTableIdx` over only the scan-like nodes present in the plan, so **an
   RTE the planner eliminated shifts every subsequent number**.

Point 4 — the ordering basis, not the counter — is the likeliest source of
divergence, and it is what this item fixes first.

### Scope

1. Number over the range-table analogue in rtindex order, including eliminated
   entries, rather than over surviving scan nodes.
2. Adopt the high-water counter with the collision re-check (`do/while`).
3. The parent-namespace preload (point 2) is **deferred** with a ledger row: it
   needs a query-level notion the renderer does not have.

Then **re-measure** `shape_mismatches` and correct 07 §2's attribution. The
figure may barely move; that is a finding to record, since the count was used to
size Phase 3.

*Gate: units + regress; re-pin the goopg-vs-goopg baseline in the same commit.*

---

## 6. P0-04b — the walkers disagree

### Problem, confirmed

Plain walker (`walkPlanFiltered`, `:413`):

```go
rowSrc := n
if attachedFilterNode != nil { rowSrc = attachedFilterNode }
est := optimizer.EstimateRows(rowSrc)      // :485-488
```

ANALYZE walker (**[R2]** `walkPlanAnalyzeFiltered`, declared at `:1499`; the
line is `:1536`, not "1520-ish"):

```go
est := optimizer.EstimateRows(n)
```

It threads `attachedFilter` and `filterRowsRemoved` but never the filter *node*.
So `EXPLAIN` and `EXPLAIN ANALYZE` print **different `rows=` for the same
filtered scan** — the plain one correct, the ANALYZE one missing the filter's
selectivity. Every ANALYZE artefact overstates the estimate on exactly the nodes
where it matters most.

**[R2]** And there are **three** walkers, not two: `planToJSON` (`:1791`) has
the same defect and additionally does not collapse the wrappers at all (§3.5).

### Design

Give the ANALYZE walker the same `attachedFilterNode` handling, and
`planToJSON` the same. The existing sibling-pair convention (`:1518-1521`:
"Both walkers must agree: a test pinning one proves nothing about the other";
`:1885-1892`) is extended to name all three. A test renders one filtered-scan
plan through all three and asserts the `rows=` agree.

*Gate: units.*

---

## 7. P0-04c — `GOOPG_INDEX_PROBE_MULT` is invisible to artefacts

`internal/optimizer/cost_funcs.go:438`:

```go
var indexProbeCostMultiplier = envFloatDefault("GOOPG_INDEX_PROBE_MULT", 1.0)
```

It multiplies index-probe cost — it shapes plans — and is in neither
`flagProvenanceOrder` nor `flagProvenanceExempt`. The guard that exists to
prevent this (`TestFlagProvenanceTableCoversPlannerEnv`) missed it because its
detector is `os\.Getenv\("(GOOPG_[A-Z0-9_]+)"\)` (`flaglabels_test.go:94`) —
reading through the `envFloatDefault` helper walks straight past it.

**The guard has a hole and this flag is the proof it has been exploited.**

### Design, in one commit

1. Replace the detector with a **`go/ast` walk over string literals** in the
   package's non-test sources — **[R2]** not a raw-text regex, which would also
   match flag names quoted in comments (e.g. `flaglabels.go:43`). This matches
   §2's own approach.
2. Add `GOOPG_INDEX_PROBE_MULT` to `flagProvenanceOrder` with a resolver
   rendering `unset(1)`, and regenerate:
   `go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env`.

An independent sweep confirmed it is the **only** helper-wrapped read and the
only unstamped, unexempt `GOOPG_*` name in the package; the other 15 are all
stamped or exempt. The commit confirms this rather than assuming it.

*Gate: `TestFlagProvenanceEnvIsGenerated`,
`TestFlagProvenanceTableCoversPlannerEnv`.*

---

## 8. Order **[R2 — regrouped]**

Revision 1 split the two label-changing items across positions 2 and 6, so the
goopg-vs-goopg baseline would be re-pinned twice with the cost work in between,
and the cost diff would be read against a baseline that had already moved.
Label changes are now adjacent and lead.

| # | item | changes rendered text? | baseline re-pin |
|---|---|---|---|
| 1 | P0-04c | no | no |
| 2 | P0-01 (+18 arms, +2 `%T` arm fixes) | **labels** | — |
| 3 | P0-04 (numbering) | **labels** | once, after item 3 |
| 4 | P0-04b (three walkers agree on `rows=`) | `rows=` in ANALYZE/JSON only | — |
| 5 | P0-02 (costs + width, text and JSON) | `cost=`, `width=` | — |
| 6 | P0-03 (above-seam derivation) | `cost=` on remaining nodes | once, after item 6 |

P0-04c leads because it changes no rendered text at all, validating the
commit/gate loop before anything observable moves.

---

## 9. Exit check **[R2 — scoped]**

Revision 1 required before/after captures to be identical modulo `cost=` and
`width=`, which items 2 and 3 contradict by design.

- **Items 2–3 (labels).** A before/after *label* diff is produced and reviewed
  line by line. Every changed label must be traceable to a named uncovered type
  or to the rtindex-order rule. Baseline re-pinned once at the end of item 3.
- **Items 4–6 (numbers).** Normalising away `cost=`, `width=` and `rows=`, the
  captures must be **identical**. Any structural difference is a planner
  behaviour change that leaked into an instrument commit, and is reverted rather
  than explained.

Corpus: TPC-H 22 (goopg :65433, up) and TPC-DS 99 (goopg :65437, to be started).

---

## 10. Risks

| risk | mitigation |
|---|---|
| **[R2]** Node reuse collapses two tree positions into one cost. | Dissolved by §3's revised mechanism: the cost is a field of the node, so a shared node has one cost because it is one node. |
| `DeriveLegacyDisplayCost` is mistaken for a cost model and cited in a later phase. | Named for what it is, comment states it is display-only and deleted by Phase 4, and it lives beside the real cost functions where that comment will be read. |
| P0-04's re-measurement shows `shape_mismatches` barely moves, invalidating a Phase 3 sizing input. | A finding, recorded in 07 §2 and REVIEW.md, not a reason to skip the item. |
| **[R2]** The 18 new `describePlan` arms invent labels PG does not use. | Each new label is taken from `explain.c`'s node-name table; a type with no PG counterpart gets its goopg name and a comment saying so. |
| **[R2]** JSON format silently keeps diverging from TEXT. | §3.5 puts the four cost keys in scope; the wrapper-collapse gap is recorded as its own item rather than left implicit. |
| **[R2]** The `stawidth` semantic mismatch is absorbed silently while wiring width. | Width uses `tupleWidth` only; the `ColumnStats.AvgWidth` vs `stawidth` mismatch gets a deferral-ledger row and stays out of this group. |
