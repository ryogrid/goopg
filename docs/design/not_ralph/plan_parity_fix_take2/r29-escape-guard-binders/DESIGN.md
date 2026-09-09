# R29 — the escaping-outer-reference guard must count binders

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN written after the fact. Recorded honestly rather than
back-dated: the round began as a narrow Q68 investigation and the
design only became clear once the PG oracle refuted the premise the
previous fix (K27) had been built on. §6 is the review record.*

## 0. The axis this round is on

Not costing — **eligibility** (K27). A query whose join tree the
PG-shaped search *declines* falls back to the legacy planner and cannot
converge on PG's plan by any amount of cost work. TPC-DS carried 9 such
declines; this round targets the one reported as `reason=lateral`.

## 1. The observation that started it

TPC-DS Q68 declines with `seam-decline reason=lateral`, yet the query
text contains **no `LATERAL` keyword** (`grep -icE lateral query68.sql`
-> 0). Its FROM is a plain derived table. Tracing the seam showed the
decline is raised for leaf 0, a `*Project` — the derived table's own
subplan root — because `nodeReferencesOuter` reported it as referencing
an outer scope.

## 2. Root cause

`planHasEscapingOuterRef` flattened the subtree with `walkPlanExprs`
and reported ANY `OuterColumnRef` whose `Level >= depth`. `walkPlanExprs`
is an expression walker: it does not model scopes, so a reference and
the thing that BINDS it look the same to it.

That became wrong when R25 slice 1 decomposed the fused NLI into
`Join{Lateral:true}` over a parameterized `IndexScan` whose probe keys
are `OuterColumnRef`s at level 1 (`outerParamKey`). Those keys are bound
by the very join above them — the executor pushes the left row onto
`ctx.OuterRows` before re-evaluating the right side — so they never
reach the enclosing scope. The guard reported them as escaping anyway,
`chainCarriesLateral` believed it, and the whole enclosing join search
was declined.

## 3. Why the existing K27 fix was not the answer

Q30 had already hit this and was patched by returning `false` for a
`*CTEScan` outright, reasoning that "a `WITH` body is planned in its own
scope and SQL gives it no way to reference the enclosing query."

**The PG 18.3 oracle refutes that reasoning.** `LATERAL` governs the
visibility of *sibling FROM items*; an enclosing *query level* is
visible regardless, by ordinary correlation:

```sql
-- accepted by PG 18.3 (:65438 tpcds05)
SELECT 1 FROM store s WHERE EXISTS (
  WITH c AS (SELECT s.s_store_sk AS k) SELECT 1 FROM c);
SELECT 1 FROM store s WHERE EXISTS (
  SELECT 1 FROM (SELECT 1 AS x FROM (SELECT s.s_store_sk) e) d);

-- rejected by PG 18.3: ERROR "missing FROM-clause entry for table s"
WITH c AS (SELECT s.s_store_sk AS k) SELECT 1 FROM store s, c;
```

So a CTE body — and equally a non-lateral derived table — CAN hold a
genuinely escaping reference, and the blanket `return false` could hide
one. It was correct on TPC-DS only because those CTEs are top-level.
The same oracle run killed the obvious generalisation of it ("mark
non-lateral derived tables as scope boundaries") before it was written.

## 4. The design

Judge a reference by **whether something inside the subtree binds it**,
not by the node kind it happens to sit under. Walk the plan
STRUCTURALLY and grow `depth` when descending through a binder:

- `Join{Lateral:true}` binds level `depth` **for its right side only**
  (the executor pushes the LEFT row, so the left side is unbound here).
- `NestedLoopIndexJoin` binds its inner by construction — the fused
  spelling of the same thing, kept in step until R25 slice 4 deletes it.
- Every other node is a scoping pass-through: children at `depth`
  unchanged, own expressions judged at `depth`.

`*CTEScan` therefore becomes an ordinary pass-through and the special
case is **deleted, not supplemented**: the body's probe keys are bound
by the lateral join inside the body, while a reference that truly
reaches out still escapes.

**Children are found by reflection, not by a type switch.** The first
implementation enumerated six node kinds and fell through to the flat
walker for the rest — including `*Aggregate`, which is the shape of
every TPC-DS CTE body, so the fallback flattened straight past the
binder. Measured: `lateral` declines went 1 -> **4**, worse than base.
Enumerating 18 single-child containers by hand would also have to be
kept in step with `walkPlanExprs` forever (the sibling-paths hazard).
The generic walk instead reads exported `Node` / `[]Node` fields, and
gets a node's OWN expressions by flat-walking a shallow copy with the
child links stubbed out — so the expression inventory still comes from
`walkPlanExprs`, the same switch every other reader uses.

Nodes that are not pointers-to-struct keep the flat, conservative
behaviour, so the change can only ever REMOVE false declines.

## 5. Gates and the category prediction (stated before measuring)

Suites green; TPC-H values digest byte-identical; TPC-DS SF0.5 sweep
all-zero; decline re-audit under a protocol identical to the base run
(the earlier "9" was taken at a 60 s timeout and is not comparable at
90 s — re-measure both sides).

Prediction: `lateral` declines 1 -> 0, no other decline class moves.
Parity categories are NOT predicted to improve: making a query eligible
only hands it to the cost model, which is the separate K26 axis. A
category may WORSEN, and that is a real result, not a regression to be
suppressed — it is a divergence the legacy fallback was hiding.

## 6. Review record

Self-review; the oracle did the adversarial work:

- **Refuted by PG, mid-round:** "a non-lateral derived table cannot
  reference the enclosing query" — the premise the round was about to
  be built on, and the premise K27 *had* been built on. Killed by three
  statements against :65438 before any code was written. §3.
- **Refuted by measurement:** the six-arm structural switch. The
  decline census called it: 1 -> 4. §4.
- **Checked, not assumed:** `planHasOuterRef`'s other consumer is
  M0058-0001's SubqueryCache constant-key decision. The new answer is
  sound there for the same reason: if every reference is bound inside
  the subquery, it yields the same rows for every outer row.
