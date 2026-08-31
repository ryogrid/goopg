# M0125-0049 — EXPLAIN prints a shared CTE body once, as a `CTE <name>` section

**Status:** implemented 2026-08-06 (`internal/executor/explain_cte.go`).
**Filed by:** `M0125-0040`, whose acceptance wording ("Q67's plan shows ONE scan
of `store_sales`") read as unmet even though the runtime claim held.

## The defect

A TPC-DS Q67 plan captured at HEAD before this change was 127 lines and
mentioned `store_sales` 36 times — for a four-table join that the engine
executes **once**. Every one of those copies was the same subtree, printed
again under each of the nine `CTE Scan on __gs_src_871` references M0125-0040's
grouping-sets rewrite creates.

This is the exact misreading `M0125-0026`'s plan capture exists to prevent: the
plan over-stated the work by the reference count, so a triage loop reading it
would classify Q67 as "nine full scans of the fact table" and go looking for a
sharing bug that had already been fixed. It predates -0040 — it affects every
multiply-referenced user CTE, and TPC-DS has 30 of them (Q1's
`customer_total_return` printed its aggregate over `store_returns ⋈ date_dim`
twice).

The cause is not a planner one. goopg does **not** clone a CTE body per
consumer, despite the M0016-0002 comment saying so: `planScanRangeVar`
(`internal/planner/planner.go`) builds every reference with `Child: ce.body`,
one shared Node. The plan is a DAG; the EXPLAIN walker walked it as a tree.

## What PostgreSQL does

`SS_process_ctes` (`optimizer/plan/subselect.c`) turns each WITH entry into a
subplan of the top plan node, and `ExplainSubPlans`
(`backend/commands/explain.c`) prints it under a `CTE <name>` heading — once,
in declaration order, after the top node's own detail lines and **before** its
InitPlans and its children. Every reference is then a childless `CTE Scan`.
Captured from PG 18.3 rather than inferred:

```
 Hash Join
   Hash Cond: (q.b = p.a)
   CTE x
     ->  Seq Scan on t
           Filter: (a > 5)
   ->  CTE Scan on x q
   ->  Hash
         ->  CTE Scan on x p
```

Three further captures pinned the edges (all in `explain_cte_test.go`):

- **one reference is still sectioned** — `WITH x AS MATERIALIZED (…) SELECT *
  FROM x` prints `CTE Scan on x` with `CTE x` beneath it, so there is no
  refs==1 special case;
- **declaration order, not encounter order** — with `y` reading `x`, PG prints
  `CTE x` then `CTE y`, though a render-order walk meets `y` first;
- **a WITH nested inside a CTE body sections inside that body**, because the
  section belongs to the declaring query level.

## The change

`internal/executor/explain_cte.go` (new), wired into both text walkers:

1. `collectCTEHoist(root)` — one pre-order pass over the plan spine that claims
   one body per CTE name, descending into a claimed body (so a CTE read only by
   another CTE gets its own section) and stopping at a repeat reference.
   Sections sort by `CTEScan.DeclSeq()`, a new accessor over a `declSeq` stamped
   in `preplanWithClause`, reproducing `SS_process_ctes`' left-to-right walk.
2. `emitCTESections` — prints `CTE <name>` at the children's indent without the
   `->  ` arrow and the body one level below it with one, from the top node
   only (`depth == 0`), before the InitPlan/SubPlan section.
3. `renderChildren` — `planChildren` everywhere except a hoisted `CTE Scan`,
   which becomes a leaf. `planChildren` itself is unchanged, so
   `explainNames.collect`'s range-table naming (`x`, `x_1`, …) and
   `resolveInAncestor`'s correlated-reference lookup still see the body.

### Why the key is the NAME, not the body pointer

The first draft keyed on body-Node identity, since a plain `WITH` shares one
Node. It under-hoisted exactly where the item came from: M0125-0040 gives each
UNION-ALL branch its own `preplanWithClause` pass, so Q67's nine references to
`__gs_src_871` carry nine **distinct, structurally identical** body Nodes — and
the capture still showed 36 `store_sales` mentions.

Name is the engine's own identity for a CTE: `ctx.CTERowCache` is
`map[string][]Row` keyed by the lowercase name, and `cteScanOp.Open` buffers on
the first scan of a name and replays for every later one. One name is one
materialisation, so one name is one section.

### The pending-sublink queue has to be held aside

`emitNodeDetailLines` assigns `SubPlan N` numbers and queues their subtrees
before the sections print. Rendering a section re-enters the walker, whose own
`emitSubPlanSubtrees` drained that queue — printing the **owner's** `SubPlan N`
inside the CTE section and deparsing its correlated references against a body
node, which regressed `TestExplainQualifiesOuterRefThroughAggregate` (M0125-0039)
to `Filter: (cst = cst)`. `emitCTESections` now takes the queue, renders, and
puts it back for the owner to drain.

## Measured

SF0.5 plan channel, full 99-query capture, two runs (the first with the pointer
key, the second with the name key):

| capture | verdict |
|---|---|
| `plans-20260806-142558` vs the -0040 baseline | `same=65 changed=34` |
| `plans-20260806-142927` (name key) vs the above | `same=91 changed=8` |

The 34 changed queries are exactly the 30 TPC-DS queries containing a `WITH`
plus the four grouping-sets queries M0125-0040 gives a synthetic CTE
(Q18/Q22/Q27/Q67). **No query without a CTE moved** — the change is confined to
rendering, and the capture says so. The eight that moved again under the name
key are the branch-per-body ones (Q5, Q14, Q18, Q22, Q27, Q67, Q77, Q80).

Corpus size 5774 → 4307 lines. Q67: 127 → 40 lines, 36 → 4 `store_sales`
mentions, one `Seq Scan on public.store_sales`, one `CTE __gs_src_871` section,
nine leaf references. M0125-0040's literal acceptance wording is now met in
rendering as well as in execution.

## Deliberate divergences (ledger rows, 2026-08-06)

1. **Sections hoist to the render root, not to the declaring query level.**
   goopg has no plan-level marker for "this level declared this WITH list", so
   a WITH nested inside a CTE body prints its section at the top instead of
   inside that body. Resume point: carry the declaring level on `plannedCTE` and
   attach sections there.
2. **A CTE referenced only from inside a sublink is not collected.** The
   collector walks the plan spine, not expression trees, so such a body still
   renders inline under its `CTE Scan`. It prints once either way — one
   reference is the only way to reach the case.
3. **Non-text formats still duplicate.** `planToJSON` /
   `planToJSONWithStats` recurse through `planChildren` unchanged, so
   `FORMAT JSON|XML|YAML` still emits the body per reference. Upstream renders
   these through the same `ExplainSubPlans` with `Subplan Name: "CTE x"` and a
   `Parent Relationship: "InitPlan"` property.

## Bug discovered while doing this (NOT fixed here)

Keying by name is the render that matches execution — and that is itself the
problem. Two **different** CTE declarations sharing a name in disjoint scopes
alias through the one `CTERowCache[name]` entry, so the second replays the
first's rows:

```sql
SELECT v FROM (WITH x AS (SELECT 1 AS v) SELECT v FROM x) a
UNION ALL
SELECT v FROM (WITH x AS (SELECT 2 AS v) SELECT v FROM x) b;
-- PG 18.3: 1, 2      goopg: 1, 1
```

Filed as a fix_plan item with a ledger row. When it is fixed, the hoist key
here has to be fixed with it (the section would need the declaration, not the
name).
